# Vision

`llm-semantic-axi` exists so that an agent can ask a codebase a question and trust the answer it gets back.
It serves agents first, and the people who run them second.
It turns a repository into a local index and returns results that are cheap for a model to read and hard to misread.
It owns exactly one thing: building and searching a semantic index of code, docs and memory.

## Nothing reports success when it did not succeed

This rule sits above every other rule here.
An empty index and a real zero-result search are never reported the same way.
An encode that produces no output is an error, not an exit 0.
A known limit is stated where the caller meets it, such as `callers` returning empty outside Go.
Call edges grow one tested language at a time, Python first, because a wrong edge is worse than none.
A test asserts the side effect, not the exit code, and it is proven able to fail.

## The CLI is the product

`llm-semantic` is the primary surface.
The MCP server shells out to the CLI, and it is a fallback for hosts that cannot run one.
Output follows the AXI principles, and TOON through `go-axi` is the format given to a model.
JSON and plain text exist for other callers, and they carry the same truth.
The MCP server stays thin and holds no logic of its own.

## Deterministic by design

The same index and the same query return the same results.
The tool leverages vector and lexical data, and it does not call a model to answer questions.
The only model in the path is the embedder, and the caller reasons about the evidence.

## Local by default

The default store is sqlite in `.index/`, pure Go, with no server, and it combines vector search with FTS5 lexical search.
Qdrant is the other store, for larger corpora and for tools such as atcr, and these two are the only stores.
No model is bundled, and setup docs carry that weight instead.
There is no daemon.
A stale index is reported with how stale it is, and it does not block the search.
Any OpenAI-compatible embedding endpoint works, and no vendor is special.
Tests run without a network, because CI has no embedding endpoint.

## A contract others can read

The index is an artifact that other tools read, and atcr reads it without ever building it.
The storage format is a versioned, published contract, so a change to it is a breaking change with a clear error.
Profiles keep code, docs, memory and sprints in separate collections with separate scopes.

## Scope

It is not a general search engine, an agent, a code host, or a replacement for grep.
It does not answer in sentences, because it returns evidence.
It stays one focused module, and the split from `llm-tools` is not undone.
It is public and MIT, and support is best effort.
Memory stays here while it shares the search engine, and it moves out if its retention or privacy needs start to bend the code.

A change aligns when it makes results more trustworthy, cheaper to read, or easier for another tool to consume.
A change should be resisted when it can report success falsely, needs a server or a vendor by default, or adds work that belongs to the caller.
