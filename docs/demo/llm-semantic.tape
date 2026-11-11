# VHS tape: a real llm-semantic session, under 30 seconds.
# Render from the repository root: vhs docs/demo/llm-semantic.tape
# Needs the release binaries on PATH and a live embedding endpoint; see docs/demo/README.md.
# The index must already exist: the tape runs only the three queries.

Output docs/demo/llm-semantic.gif

Set Shell "bash"
Set FontSize 16
Set Width 1200
Set Height 640
Set Padding 16
Set TypingSpeed 40ms

Type "llm-semantic --version"
Enter
Sleep 2s

Type 'llm-semantic search "retry embedding requests with backoff" --axi --top 3'
Enter
Sleep 10s

Type "clear"
Enter
Type "llm-semantic callers EnrichForEmbedding --axi"
Enter
Sleep 6s
