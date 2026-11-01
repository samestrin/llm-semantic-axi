package commands

import (
	"context"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/samestrin/llm-semantic-axi/internal/semantic"
	"github.com/spf13/cobra"
)

func callersCmd() *cobra.Command {
	var (
		jsonOutput bool
		minOutput  bool
	)

	cmd := &cobra.Command{
		Use:   "callers <symbol>",
		Short: "Show what calls a symbol",
		Long: `List the functions and methods that call a symbol.

Answers come from the call graph recorded while indexing, so the index has to be
current. A call is only attributed when exactly one indexed symbol carries the
name, because a confidently wrong answer is worse than none.

Example:
  llm-semantic callers ResolveRefs`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runCallers(cmd.Context(), args[0], jsonOutput, minOutput)
		},
	}

	cmd.Flags().BoolVar(&jsonOutput, "json", false, "Output as JSON")
	cmd.Flags().BoolVar(&minOutput, "min", false, "Minimal output format")

	return cmd
}

func refsCmd() *cobra.Command {
	var (
		jsonOutput bool
		minOutput  bool
	)

	cmd := &cobra.Command{
		Use:   "refs <symbol>",
		Short: "Show what a symbol references",
		Long: `List the calls, imports and type references made by a symbol.

References to something outside the index, such as a standard library call, are
listed and marked external rather than dropped.

Example:
  llm-semantic refs ResolveRefs`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runRefs(cmd.Context(), args[0], jsonOutput, minOutput)
		},
	}

	cmd.Flags().BoolVar(&jsonOutput, "json", false, "Output as JSON")
	cmd.Flags().BoolVar(&minOutput, "min", false, "Minimal output format")

	return cmd
}

func runCallers(ctx context.Context, symbol string, jsonOutput, minOutput bool) error {
	rs, cleanup, err := openRefStorage(ctx)
	if err != nil {
		return err
	}
	defer cleanup()

	edges, err := rs.GetCallersByName(ctx, symbol)
	if err != nil {
		return fmt.Errorf("failed to look up callers of %q: %w", symbol, err)
	}

	zero, err := diagnoseZeroEdges(ctx, rs, symbol, "callers of", len(edges))
	if err != nil {
		return err
	}

	return formatCallers(os.Stdout, symbol, edges, zero, jsonOutput, minOutput)
}

func runRefs(ctx context.Context, symbol string, jsonOutput, minOutput bool) error {
	rs, cleanup, err := openRefStorage(ctx)
	if err != nil {
		return err
	}
	defer cleanup()

	edges, err := rs.GetRefsByName(ctx, symbol)
	if err != nil {
		return fmt.Errorf("failed to look up references from %q: %w", symbol, err)
	}

	zero, err := diagnoseZeroEdges(ctx, rs, symbol, "references from", len(edges))
	if err != nil {
		return err
	}

	return formatRefs(os.Stdout, symbol, edges, zero, jsonOutput, minOutput)
}

// zeroEdges says what an empty callers or refs answer means. Without it the
// three cases read the same: the symbol is not indexed, it is indexed in a
// language the index records no edges for, or it genuinely has none recorded.
// Only the last one means "none exist".
type zeroEdges struct {
	Status  string
	Message string
}

const (
	zeroSymbolNotIndexed   = "symbol_not_indexed"
	zeroNotIndexedForEdges = "not_indexed_for_edges"
	zeroNoneRecorded       = "none_recorded"
)

// diagnoseZeroEdges looks up the chunks named symbol when an answer came back
// empty, and returns nil when it did not. noun reads "callers of" or
// "references from".
func diagnoseZeroEdges(ctx context.Context, rs semantic.RefStorage, symbol, noun string, count int) (*zeroEdges, error) {
	if count > 0 {
		return nil, nil
	}

	langs, err := rs.SymbolLanguages(ctx, symbol)
	if err != nil {
		return nil, fmt.Errorf("failed to look up %s: %w", symbol, err)
	}
	if len(langs) == 0 {
		return &zeroEdges{
			Status:  zeroSymbolNotIndexed,
			Message: fmt.Sprintf("No %s %s: symbol not indexed, no chunk is named %s", noun, symbol, symbol),
		}, nil
	}

	var withEdges, without []string
	for _, l := range langs {
		name := l.Language
		if name == "" {
			name = "unknown language"
		}
		if l.HasEdges {
			withEdges = append(withEdges, name)
		} else {
			without = append(without, name)
		}
	}

	if len(withEdges) == 0 {
		return &zeroEdges{
			Status: zeroNotIndexedForEdges,
			Message: fmt.Sprintf("No %s %s: not indexed for edges (%s is indexed as %s, and the index records no edges for it), so this does not mean none exist",
				noun, symbol, symbol, strings.Join(without, ", ")),
		}, nil
	}

	msg := fmt.Sprintf("No %s %s recorded in the call graph", noun, symbol)
	if len(without) > 0 {
		msg += fmt.Sprintf("; its %s chunks are not indexed for edges", strings.Join(without, ", "))
	}
	return &zeroEdges{Status: zeroNoneRecorded, Message: msg}, nil
}

// openRefStorage opens the configured backend and returns its call graph view,
// along with a cleanup function the caller must invoke.
func openRefStorage(ctx context.Context) (semantic.RefStorage, func(), error) {
	indexPath := ""
	if storageType == "" || storageType == "sqlite" {
		indexPath = findIndexPath()
		if indexPath == "" {
			return nil, nil, fmt.Errorf("semantic index not found. Run 'llm-semantic index' first")
		}
	}

	embedder, err := createEmbedder()
	if err != nil {
		return nil, nil, fmt.Errorf("failed to create embedder: %w", err)
	}

	embeddingDim, err := probeEmbeddingDim(ctx, embedder)
	if err != nil {
		return nil, nil, err
	}

	storage, err := createStorage(indexPath, embeddingDim)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to open index: %w", err)
	}

	rs, ok := storage.(semantic.RefStorage)
	if !ok {
		storage.Close()
		return nil, nil, fmt.Errorf("storage backend %q does not record a call graph", storageType)
	}

	return rs, func() { storage.Close() }, nil
}

// formatCallers prints the callers of symbol. zero is the diagnosis of an
// empty answer; with none, the empty text can only say the symbol may not be
// indexed.
func formatCallers(w io.Writer, symbol string, edges []semantic.RefEdge, zero *zeroEdges, jsonOutput, minOutput bool) error {
	if jsonOutput || minOutput || GlobalAXIOutput {
		return writeCallersJSON(w, symbol, edges, zero, minOutput)
	}

	if len(edges) == 0 {
		if zero != nil {
			fmt.Fprintf(w, "%s (0 callers).\n", zero.Message)
			return nil
		}
		fmt.Fprintf(w, "No callers of %s (the symbol may not be indexed)\n", symbol)
		return nil
	}

	fmt.Fprintf(w, "%d caller(s) of %s\n\n", len(edges), symbol)
	for _, e := range edges {
		fmt.Fprintf(w, "  %s:%d  %s\n", e.FilePath, e.StartLine, e.Name)
	}
	return nil
}

func writeCallersJSON(w io.Writer, symbol string, edges []semantic.RefEdge, zero *zeroEdges, minimal bool) error {
	callers := make([]map[string]interface{}, 0, len(edges))
	for _, e := range edges {
		entry := map[string]interface{}{
			"file": e.FilePath,
			"line": e.StartLine,
			"name": e.Name,
		}
		if !minimal {
			entry["type"] = string(e.RefType)
			entry["ref"] = e.RefName
		}
		callers = append(callers, entry)
	}

	payload := map[string]interface{}{
		"symbol":  symbol,
		"count":   len(callers),
		"callers": callers,
	}
	addZeroEdges(payload, zero)
	return emitJSON(w, payload, true)
}

// collapseRefEdges reports each referenced name once. A method call records
// both a call edge and a uses_type edge under the same name, so listing every
// edge shows most references twice. The call edge wins where both exist;
// distinct names, such as an import, are left alone.
func collapseRefEdges(edges []semantic.RefEdge) []semantic.RefEdge {
	position := make(map[string]int, len(edges))
	out := make([]semantic.RefEdge, 0, len(edges))

	for _, e := range edges {
		i, seen := position[e.RefName]
		if !seen {
			position[e.RefName] = len(out)
			out = append(out, e)
			continue
		}
		if out[i].RefType != semantic.RefCalls && e.RefType == semantic.RefCalls {
			out[i] = e
		}
	}

	return out
}

// formatRefs prints the references from symbol; zero is as for formatCallers.
func formatRefs(w io.Writer, symbol string, edges []semantic.RefEdge, zero *zeroEdges, jsonOutput, minOutput bool) error {
	edges = collapseRefEdges(edges)

	if jsonOutput || minOutput || GlobalAXIOutput {
		return writeRefsJSON(w, symbol, edges, zero, minOutput)
	}

	if len(edges) == 0 {
		if zero != nil {
			fmt.Fprintf(w, "%s (0 references).\n", zero.Message)
			return nil
		}
		fmt.Fprintf(w, "No references from %s (the symbol may not be indexed)\n", symbol)
		return nil
	}

	fmt.Fprintf(w, "%d reference(s) from %s\n\n", len(edges), symbol)
	for _, e := range edges {
		if e.Resolved() {
			fmt.Fprintf(w, "  %-10s %-24s %s:%d\n", e.RefType, e.RefName, e.FilePath, e.StartLine)
			continue
		}
		fmt.Fprintf(w, "  %-10s %-24s (external)\n", e.RefType, e.RefName)
	}
	return nil
}

func writeRefsJSON(w io.Writer, symbol string, edges []semantic.RefEdge, zero *zeroEdges, minimal bool) error {
	refs := make([]map[string]interface{}, 0, len(edges))
	for _, e := range edges {
		entry := map[string]interface{}{
			"ref":      e.RefName,
			"type":     string(e.RefType),
			"resolved": e.Resolved(),
		}
		if e.Resolved() {
			entry["file"] = e.FilePath
			entry["line"] = e.StartLine
			if !minimal {
				entry["name"] = e.Name
			}
		}
		refs = append(refs, entry)
	}

	payload := map[string]interface{}{
		"symbol":     symbol,
		"count":      len(refs),
		"references": refs,
	}
	addZeroEdges(payload, zero)
	return emitJSON(w, payload, true)
}

// addZeroEdges puts the diagnosis of an empty answer into --axi output, where
// an agent can branch on the status. --json keeps its schema (board decision,
// 2026-10-31), so it gains no key.
func addZeroEdges(payload map[string]interface{}, zero *zeroEdges) {
	if zero == nil || !GlobalAXIOutput {
		return
	}
	payload["status"] = zero.Status
	payload["message"] = zero.Message
}
