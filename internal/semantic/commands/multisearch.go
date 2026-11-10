package commands

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/samestrin/llm-semantic-axi/internal/semantic"
	"github.com/samestrin/llm-semantic-axi/internal/semantic/config"
	"github.com/spf13/cobra"
)

func multisearchCmd() *cobra.Command {
	var (
		topK         int
		threshold    float64
		profiles     []string
		noBoost      bool
		noDedupe     bool
		outputFormat string
		jsonOutput   bool
		minOutput    bool
		full         bool
	)

	cmd := &cobra.Command{
		Use:   "multisearch <query1> [query2] [query3] ...",
		Short: "Execute multiple semantic queries with deduplication and boosting",
		Long: `Execute multiple semantic queries in parallel with intelligent deduplication and multi-match boosting.

Results matching multiple queries receive boosted scores (+0.05 per additional match).
Duplicate chunks are deduplicated, keeping the highest score.

Examples:
  llm-semantic multisearch "authentication" "JWT tokens"
  llm-semantic multisearch "error handling" "logging" --profiles code,docs
  llm-semantic multisearch "api endpoint" "REST handler" --top 20 --json
  llm-semantic multisearch "test" "spec" --output by_query`,
		Args: cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			queries := args

			// Validate queries
			if len(queries) > semantic.MaxQueries {
				return fmt.Errorf("multisearch supports up to %d queries, got: %d", semantic.MaxQueries, len(queries))
			}

			// Filter empty queries
			var filteredQueries []string
			for _, q := range queries {
				q = strings.TrimSpace(q)
				if q != "" {
					filteredQueries = append(filteredQueries, q)
				}
			}
			if len(filteredQueries) == 0 {
				return fmt.Errorf("at least one non-empty query required")
			}

			// Validate output format
			if outputFormat != "" && !semantic.IsValidOutputFormat(outputFormat) {
				return fmt.Errorf("invalid output format: %s (valid: %s)", outputFormat, strings.Join(semantic.ValidOutputFormats(), ", "))
			}

			// Validate profile names
			for _, p := range profiles {
				if !config.IsValidProfile(p) {
					return fmt.Errorf("invalid profile: %s (valid: %s)", p, strings.Join(config.ValidProfiles(), ", "))
				}
			}

			return runMultisearch(cmd.Context(), filteredQueries, multisearchOpts{
				topK:         topK,
				threshold:    float32(threshold),
				profiles:     profiles,
				noBoost:      noBoost,
				noDedupe:     noDedupe,
				outputFormat: semantic.OutputFormat(outputFormat),
				jsonOutput:   jsonOutput,
				minOutput:    minOutput,
				full:         full,
			})
		},
	}

	cmd.Flags().IntVarP(&topK, "top", "n", 15, "Maximum number of results to return (default 15 vs search's 10 to account for multi-query result merging)")
	cmd.Flags().Float64VarP(&threshold, "threshold", "t", 0.0, "Minimum similarity score (0.0-1.0)")
	cmd.Flags().StringSliceVar(&profiles, "profiles", nil, "Profiles to search across (comma-separated, e.g., code,docs)")
	cmd.Flags().BoolVar(&noBoost, "no-boost", false, "Disable multi-match score boosting")
	cmd.Flags().BoolVar(&noDedupe, "no-dedupe", false, "Disable result deduplication")
	cmd.Flags().StringVar(&outputFormat, "output", "", "Output format: blended (default), by_query, by_collection")
	cmd.Flags().BoolVar(&jsonOutput, "json", false, "Output results as JSON")
	cmd.Flags().BoolVar(&minOutput, "min", false, "Output minimal JSON format")
	cmd.Flags().BoolVar(&full, "full", false, "With --axi, return every field and the whole content of each result")

	return cmd
}

type multisearchOpts struct {
	topK         int
	threshold    float32
	profiles     []string
	noBoost      bool
	noDedupe     bool
	outputFormat semantic.OutputFormat
	jsonOutput   bool
	minOutput    bool
	full         bool
}

func runMultisearch(ctx context.Context, queries []string, opts multisearchOpts) error {
	// Initialize common search components
	components, cleanup, err := initSearchComponents(ctx, createStorage)
	if err != nil {
		return err
	}
	defer cleanup()

	// Create searcher
	searcher := semantic.NewSearcher(components.Storage, components.Embedder)

	// Build multisearch options
	boostEnabled := !opts.noBoost
	msOpts := semantic.MultisearchOptions{
		Queries:         queries,
		TopK:            opts.topK,
		Threshold:       opts.threshold,
		Profiles:        opts.profiles,
		BoostMultiMatch: &boostEnabled,
		Output:          opts.outputFormat,
	}

	// Execute multisearch
	result, err := searcher.Multisearch(ctx, msOpts)
	if err != nil {
		return fmt.Errorf("multisearch failed: %w", err)
	}
	indexEmpty, err := zeroResultsIndexEmpty(ctx, components.Storage, result.TotalResults)
	if err != nil {
		return err
	}
	stale := indexStaleness(ctx, components.Storage)

	// Apply output format if specified
	if opts.outputFormat != "" && opts.outputFormat != semantic.OutputBlended {
		result = result.FormatAs(opts.outputFormat)
	}

	// Output results
	if opts.jsonOutput || opts.minOutput || GlobalAXIOutput {
		return outputMultisearchJSON(queries, result, opts.minOutput, opts.full, indexEmpty, stale)
	}
	if err := outputMultisearchText(queries, result, indexEmpty); err != nil {
		return err
	}
	printStalenessLine(stale, false)
	return nil
}

// noMultisearchResults is the zero-result sentence for plain and --axi.
func noMultisearchResults(queries []string) string {
	if len(queries) == 1 {
		return noResultsFor(queries[0]) + "."
	}
	return fmt.Sprintf("No results found for any of the %d queries.", len(queries))
}

func outputMultisearchJSON(queries []string, result *semantic.MultisearchResult, minimal, full, indexEmpty bool, stale semantic.Staleness) error {
	payload := multisearchPayload(result, minimal)
	if GlobalAXIOutput && !minimal && !full {
		payload = multisearchAXIPayload(result)
	}
	if !GlobalAXIOutput {
		printStalenessLine(stale, true)
		return emitStdout(payload)
	}

	// --json already carries total_results; --axi adds the sentence, so an
	// empty answer reads as a search that ran and matched nothing. The payload
	// goes through its JSON form so --axi keeps every key --json has.
	b, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	var withMessage map[string]any
	if err := json.Unmarshal(b, &withMessage); err != nil {
		return err
	}
	withMessage["help"] = resultHelp(result.TotalResults)
	if result.TotalResults == 0 {
		withMessage["message"] = noMultisearchResults(queries)
		markZeroResults(withMessage, indexEmpty)
	}
	withMessage["stale"] = axiStaleness(stale)
	return emitStdout(withMessage)
}

// multisearchAXIPayload is the default --axi multisearch body: the totals
// --json carries, with each hit cut to file, name, score and content. score is
// the boosted score, the one the list is ranked by. --full keeps the raw result.
func multisearchAXIPayload(result *semantic.MultisearchResult) map[string]any {
	rows := func(rs []semantic.EnhancedResult) []map[string]any {
		out := make([]map[string]any, len(rs))
		for i, r := range rs {
			out[i] = map[string]any{
				"file":    fmt.Sprintf("%s:%d", r.Chunk.FilePath, r.Chunk.StartLine),
				"name":    r.Chunk.Name,
				"score":   r.BoostedScore,
				"content": axiCut(r.Chunk.Content),
			}
		}
		return out
	}
	payload := map[string]any{
		"total_queries":   result.TotalQueries,
		"total_results":   result.TotalResults,
		"queries_matched": result.QueriesMatched,
	}
	if result.Format != "" {
		payload["format"] = result.Format
	}
	if result.Results != nil {
		payload["results"] = rows(result.Results)
	}
	if result.ByQuery != nil {
		g := map[string][]map[string]any{}
		for k, v := range result.ByQuery {
			g[k] = rows(v)
		}
		payload["by_query"] = g
	}
	if result.ByCollection != nil {
		g := map[string][]map[string]any{}
		for k, v := range result.ByCollection {
			g[k] = rows(v)
		}
		payload["by_collection"] = g
	}
	return payload
}

// multisearchPayload is what --json and --min print.
func multisearchPayload(result *semantic.MultisearchResult, minimal bool) any {
	if minimal {
		// Minimal format: just essential fields
		minResult := map[string]interface{}{
			"total_queries": result.TotalQueries,
			"total_results": result.TotalResults,
		}

		if result.Results != nil {
			minResults := make([]map[string]interface{}, len(result.Results))
			for i, r := range result.Results {
				minResults[i] = map[string]interface{}{
					"file":    r.Chunk.FilePath,
					"name":    r.Chunk.Name,
					"line":    r.Chunk.StartLine,
					"score":   r.Score,
					"boosted": r.BoostedScore,
					"matches": len(r.MatchedQueries),
				}
				if r.Chunk.Domain != "" {
					minResults[i]["profile"] = r.Chunk.Domain
				}
			}
			minResult["results"] = minResults
		}

		if result.ByQuery != nil {
			minByQuery := make(map[string][]map[string]interface{})
			for query, results := range result.ByQuery {
				minByQuery[query] = make([]map[string]interface{}, len(results))
				for i, r := range results {
					minByQuery[query][i] = map[string]interface{}{
						"file":    r.Chunk.FilePath,
						"name":    r.Chunk.Name,
						"line":    r.Chunk.StartLine,
						"score":   r.Score,
						"boosted": r.BoostedScore,
					}
				}
			}
			minResult["by_query"] = minByQuery
		}

		if result.ByCollection != nil {
			minByCollection := make(map[string][]map[string]interface{})
			for collection, results := range result.ByCollection {
				minByCollection[collection] = make([]map[string]interface{}, len(results))
				for i, r := range results {
					minByCollection[collection][i] = map[string]interface{}{
						"file":    r.Chunk.FilePath,
						"name":    r.Chunk.Name,
						"line":    r.Chunk.StartLine,
						"score":   r.Score,
						"boosted": r.BoostedScore,
					}
				}
			}
			minResult["by_collection"] = minByCollection
		}

		return minResult
	}

	return result
}

func outputMultisearchText(queries []string, result *semantic.MultisearchResult, indexEmpty bool) error {
	fmt.Printf("Multisearch: %d queries, %d results\n\n", result.TotalQueries, result.TotalResults)

	// The total, not the blended list: a grouped format leaves that list
	// empty by design, and checking it printed "No results found." under a
	// header that counted results.
	if indexEmpty {
		fmt.Println(emptyIndexMessage)
		return nil
	}
	if result.TotalResults == 0 {
		fmt.Println(noMultisearchResults(queries))
		return nil
	}

	// Grouped formats leave the blended list empty by design, so the groups
	// themselves must be rendered: a heading per group, then the same
	// per-result lines the blended output prints. Without this the grouped
	// text output printed only the header above.
	group := 0
	printGroup := func(heading string, rs []semantic.EnhancedResult) {
		group++
		fmt.Printf("%s (%d result%s)\n", heading, len(rs), pluralS(len(rs) != 1))
		for i, r := range rs {
			printMultisearchResult(i+1, r)
		}
	}
	if result.Format == semantic.OutputByQuery {
		// The query order the caller gave, not map order.
		for _, q := range queries {
			printGroup("Query: "+q, result.ByQuery[q])
		}
		return nil
	}
	if result.Format == semantic.OutputByCollection {
		for _, name := range sortedKeys(result.ByCollection) {
			printGroup("Collection: "+name, result.ByCollection[name])
		}
		return nil
	}

	for i, r := range result.Results {
		printMultisearchResult(i+1, r)
	}
	return nil
}

// pluralS returns "s" when the count needs one, for the group headings.
func pluralS(needsS bool) string {
	if needsS {
		return "s"
	}
	return ""
}

// sortedKeys is the map ordering the grouped text output needs, so the
// headings do not shuffle between runs.
func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// printMultisearchResult prints one result: file:line, the symbol, the
// scores, and the queries it matched.
func printMultisearchResult(i int, r semantic.EnhancedResult) {
	{
		// Format match info
		matchInfo := ""
		if len(r.MatchedQueries) > 1 {
			matchInfo = fmt.Sprintf(" [%d matches]", len(r.MatchedQueries))
		}

		// Format profile info
		profileInfo := ""
		if r.Chunk.Domain != "" {
			profileInfo = fmt.Sprintf(" (%s)", r.Chunk.Domain)
		}

		fmt.Printf("%d. %s:%d - %s%s%s\n", i, r.Chunk.FilePath, r.Chunk.StartLine, r.Chunk.Name, profileInfo, matchInfo)
		fmt.Printf("   Score: %.4f (boosted: %.4f)\n", r.Score, r.BoostedScore)

		if len(r.MatchedQueries) > 0 {
			fmt.Printf("   Matched: %s\n", strings.Join(r.MatchedQueries, ", "))
		}

		// Show preview if available
		if r.Preview != "" {
			fmt.Printf("   %s\n", r.Preview)
		} else if r.Chunk.Signature != "" {
			fmt.Printf("   %s\n", r.Chunk.Signature)
		}
		fmt.Println()
	}
}
