# Release checklist

Complete these steps before tagging a release.

- [ ] **Re-render the demo when a command it shows changes.** Update `docs/demo/llm-semantic.tape` if the command or its output has changed, then run from the repository root:

  ```bash
  vhs docs/demo/llm-semantic.tape
  ```

  Check that the recording is under 30 seconds and commit the regenerated `docs/demo/llm-semantic.gif`. See `docs/demo/README.md` for the required release binary, VHS dependencies, embedding endpoint and index.

- [ ] **Re-run the performance benchmark and commit the results.** From the repository root, run:

  ```bash
  make bench-perf-write
  ```

  Commit the regenerated `benchmarks/results-performance.md` and `benchmarks/results-performance.json`. The command builds the release binary, measures it against a loopback fake embedder and rewrites both files.
