# Demo

`llm-semantic.tape` is a [VHS](https://github.com/charmbracelet/vhs) script for a real session of under 30 seconds: `--version`, a `search` with `--axi`, and a `callers` query. Nothing in it is mocked; every command runs against the release binary. `llm-semantic.gif` is its rendered output, committed as a file in this directory rather than linked from `vhs.charm.sh`.

## What the session needs

- `llm-semantic` on `PATH` (the release binary; see the README's Install section).
- `vhs`, plus the `ttyd` and `ffmpeg` it drives.
- A reachable OpenAI-compatible **embedding endpoint**. `search` embeds the query at run time, so it fails without one. The tape does not set one; export these before rendering:

  ```bash
  export LLM_SEMANTIC_API_URL=http://localhost:8080   # your TEI / Ollama / vLLM endpoint
  export LLM_SEMANTIC_MODEL=bge-small-en-v1.5         # the model that endpoint serves
  ```

- An index of this repository, built with the same endpoint and model, before recording. The tape runs only the queries, so indexing time is not in the 30 seconds:

  ```bash
  llm-semantic index .   # writes .index/, which is gitignored
  ```

`callers EnrichForEmbedding` reads the call graph recorded at index time. Go is the language that records call edges, which is why a Go symbol is queried; an empty answer for another language would mean "not indexed", not "no callers".

## Render

From the repository root:

```bash
vhs docs/demo/llm-semantic.tape
```

This writes `docs/demo/llm-semantic.gif`. Check the result is under 30 seconds before committing it.
