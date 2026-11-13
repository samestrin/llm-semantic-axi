# Storage Contract — Version 1

This document is the external contract for the data `llm-semantic` writes into a
Qdrant collection. External consumers (such as `atcr`) may rely on the point
kinds, payload keys, and value shapes described here. Every key and shape is
cited to the function that writes it, in `internal/semantic/storage_qdrant.go`
unless noted otherwise. Citations name symbols, not line numbers, so they do not
drift as the file changes; a test checks every cited symbol is still declared.

**Scope of version 1:** this contract covers the **Qdrant store only**
(the collection selected by `--collection`, default `llm_semantic`). The
default storage backend is sqlite (`--storage sqlite`, resolved by
`resolveStorageType` in `internal/semantic/commands/root.go`), whose on-disk
file is `.index/semantic.db` (`IndexPath` in
`internal/semantic/storage_sqlite.go`). The sqlite file is an internal
implementation detail and is **not** part of this contract.

## Point ID derivation

Chunk, `file_hash`, `calibration_metadata`, `last_index`, `storage_contract`, and memory point IDs are
deterministic UUIDs derived from a string by SHA-256: `stringToUUID(s)` hashes
`s` with SHA-256 and formats bytes `0:4-4:6-6:8-8:10-10:16` as a UUID. The
exception is `retrieval_log` points, whose IDs are time-based and not stable:
`retrievalLogID` (in `storage_qdrant_stats.go`) hashes `"log:<unix-nanos>"`.

## The six point kinds

One Qdrant collection holds six kinds of points. They are distinguished by
payload, not by collection or by ID namespace alone.

### 1. Chunk points (code chunks)

Written by `chunkToPoint`.

- **ID:** `stringToUUID(chunk.ID)`.
- **Vector:** the real embedding of the chunk content.
- **Payload:**

| Key | Type |
| --- | --- |
| `chunk_id` | string — original chunk ID |
| `file_path` | string |
| `type` | string — `ChunkType.String()` (`internal/semantic/types.go`): the values emitted by the registered chunkers are `function`, `method`, `struct`, `interface`, `file`; the default branch also returns `unknown` for any out-of-range value |
| `name` | string |
| `signature` | string |
| `content` | string — full chunk text |
| `start_line` | int |
| `end_line` | int |
| `language` | string |
| `domain` | string — defaults to `"code"` when unset |
| `content_hash` | string |

### 2. `file_hash` points (incremental-index bookkeeping)

Written by `SetFileHashWithFallback`, which `SetFileHash` calls.

- **ID:** `stringToUUID("file_hash:" + filePath)` (`fileHashID`).
- **Vector:** a zero vector of `embeddingDim` floats — a dummy, never
  semantically meaningful.
- **Payload:**

| Key | Type |
| --- | --- |
| `type` | string — always `"file_hash"` |
| `file_path` | string |
| `content_hash` | string — hash of the file's content at index time |
| `fallback_reason` | string — why the file was chunked as text windows: `"parse_failed"` or `"chunker_failed"`. Written only for a file that fell back; absent otherwise, and in points written before it was recorded. Written by `SetFileHashWithFallback`, read by `ListFallbackFiles` and, for one file, `GetFallbackReason`. Added after version 1; additive, so the version is unchanged |

### 3. The `calibration_metadata` point (singleton)

Written by `SetCalibrationMetadata`; restored across collection clears by
`Clear`.

- **ID:** `stringToUUID("calibration_metadata")` (`calibrationMetadataID`).
  Exactly one such point exists per collection.
- **Vector:** a zero vector of `embeddingDim` floats — a dummy.
- **Payload:**

| Key | Type |
| --- | --- |
| `type` | string — always `"calibration_metadata"` |
| `calibration_json` | string — JSON-serialized `CalibrationMetadata` |

The JSON in `calibration_json` has these fields (`CalibrationMetadata` in
`internal/semantic/calibration.go`): `embedding_model` (string),
`calibration_date` (RFC 3339 timestamp), `perfect_match_score` (float),
`baseline_score` (float), `score_range` (float), `high_threshold` (float),
`medium_threshold` (float), `low_threshold` (float).

### 4. Memory entry points

Written by `memoryToPoint`.

- **ID:** `stringToUUID("memory:" + entry.ID)` (`memoryPointID`).
- **Vector:** a **real** embedding — memory entries are semantically
  searchable, unlike the zero-vector bookkeeping points.
- **Payload.** Note: memory points carry **no `type` key**; their
  discriminator is `entry_type`.

Keys written at creation:

| Key | Type |
| --- | --- |
| `memory_id` | string — original entry ID |
| `question` | string |
| `answer` | string |
| `tags` | string — comma-joined |
| `source` | string |
| `status` | string — a `MemoryStatus` (`internal/semantic/memory_entry.go`): `pending` or `promoted` |
| `occurrences` | int |
| `entry_type` | string — always `"memory"` |
| `created_at` | string |
| `updated_at` | string |
| `file_path` | string |
| `sprints` | string — comma-joined |
| `files` | string — comma-joined |

Keys added or rewritten later (all in
`internal/semantic/storage_qdrant_stats.go`):

| Key | Type | Written by |
| --- | --- | --- |
| `retrieval_count` | int — number of tracked retrievals; absent until the first one | `TrackMemoryRetrieval`, through `SetPayload` |
| `avg_score` | double — running mean of retrieval scores; absent until the first tracked retrieval | `TrackMemoryRetrieval`, through `SetPayload` |
| `last_retrieved` | string — RFC 3339 timestamp; absent until the first tracked retrieval | `TrackMemoryRetrieval`, through `SetPayload` |
| `status` | string — rewritten | `UpdateMemoryStatsStatus` |

### 5. `retrieval_log` points (memory retrieval events)

Written by `TrackMemoryRetrieval` (in `storage_qdrant_stats.go`) into the
same collection, once per tracked memory retrieval;
`PruneMemoryRetrievalLog` scrolls and deletes them.

- **ID:** time-based, not deterministic — `stringToUUID("log:<unix-nanos>")`
  (`retrievalLogID`).
- **Vector:** a zero vector of `embeddingDim` floats — a dummy.
- **Payload:**

| Key | Type |
| --- | --- |
| `entry_type` | string — always `"retrieval_log"` |
| `memory_id` | string — the retrieved memory's original ID |
| `query` | string |
| `score` | double — retrieval score |
| `timestamp` | string — RFC 3339 |

Note the shared `memory_id` key: a `retrieval_log` point is not a memory
entry. Select memory entries by `entry_type == "memory"`, not by the presence
of `memory_id`.

### 6. The `last_index` point (singleton)

The commit and time of the last index that brought the whole collection up to
that commit. Written by `SetLastIndex`, with the payload built by
`setLastIndexLocked`; read by `getLastIndexLocked`, which `Stats` uses to
report `last_updated`; removed by a collection clear (`Clear`), which unlike
`calibration_metadata` does not restore it: a re-index that stops after the
clear must leave no record, so it reads as unknown rather than current, as the
sqlite backend does. Added after version 1 was published; it is additive, so
the version is unchanged.

- **ID:** `stringToUUID("last_index")` (`lastIndexID`). At most one such point
  exists per collection; it is absent until the first index is recorded.
- **Vector:** a zero vector of `embeddingDim` floats — a dummy.
- **Payload:**

| Key | Type |
| --- | --- |
| `type` | string — always `"last_index"` |
| `commit` | string — the full commit hash of `HEAD` when the index started; empty when the indexed directory is not in a git repository |
| `indexed_at` | string — RFC 3339 timestamp in UTC, with fractional seconds, of when the index started |
| `root` | string — absolute path of the indexed directory, which the staleness check measures from; empty or absent in a point written before it was recorded, in which case the check measures from the working directory. Added after version 1; additive, so the version is unchanged |

### 7. The `storage_contract` point (singleton)

Records the `contract_version` of this document that wrote the collection.
Written by `ensureContractVersion` whenever the collection is opened or
recreated (including after a collection clear); a collection with no such point
predates it, is version 1 by definition, and is stamped on its next open. On
open, `llm-semantic` reads the point and refuses to use the collection with a
clear error when the stored version differs from the one it implements
(`StorageContractVersion`). Added after version 1 was published; it is
additive, so the version is unchanged.

- **ID:** `stringToUUID("storage_contract")` (`contractID`). At most one such
  point exists per collection.
- **Vector:** a zero vector of `embeddingDim` floats — a dummy.
- **Payload** (built by `contractPoint`):

| Key | Type |
| --- | --- |
| `type` | string — always `"storage_contract"` |
| `contract_version` | int — the version in this document's title |

## Filter rule: how to select chunks

**Chunks are exactly the points that carry a `chunk_id` payload key.** Only
`chunkToPoint` writes `chunk_id`; none of the other five kinds do. The read
path relies on the same key: `payloadToChunk` prefers `chunk_id` from the
payload over the point UUID.

**`type` is not chunk-exclusive.** `file_hash` points, the
`calibration_metadata` point and the `last_index` point also carry a `type`
key, while memory points carry none at all. Filtering on the presence of
`type`, or treating every `type`-bearing point as a chunk, will misclassify
points. To select:

- chunks → points having `chunk_id` (this is how `Stats` counts them for
  `chunks_total` and counts their distinct `file_path` for `files_indexed`;
  `chunk_id` carries a keyword payload index, created by `ensureCollection` on
  every open);
- file-hash bookkeeping → `type == "file_hash"` (this is how
  `ListIndexedFiles` selects them);
- calibration metadata → `type == "calibration_metadata"` (read by ID in
  `GetCalibrationMetadata`);
- the last index record → `type == "last_index"` (read by ID in
  `getLastIndexLocked`);
- the contract marker → `type == "storage_contract"` (read by ID);
- memory entries → `entry_type == "memory"` (this is how `SearchMemory` and
  `ListMemory` filter; also the decode guard in `payloadToMemory`);
- retrieval logs → `entry_type == "retrieval_log"` (written by
  `TrackMemoryRetrieval`).

Consumers doing vector search over chunk points should also be aware that
zero-vector points (`file_hash`, `calibration_metadata`, `last_index`,
`retrieval_log`)
exist in the same collection; the `chunk_id` presence rule is the reliable
way to exclude them.

## Bumping this contract

This document carries a `contract_version`, stated in the title
(`Storage Contract — Version 1`). The rule:

- **A breaking change raises `contract_version` and is listed in
  `CHANGELOG.md`.** Breaking means anything an existing consumer may rely on
  changing underneath it: a point kind removed or split, a payload key
  renamed or removed, a value type changed, an ID-derivation rule changed, or
  the filter rule changed (for example chunks no longer selected by
  `chunk_id` presence).
- **Additive changes do not bump the version.** Adding a new payload key to
  an existing kind, adding a new point kind alongside the existing ones, or
  documenting a previously undocumented shape leaves `contract_version` where
  it is, because a consumer written against an older version still reads the
  collection correctly.
- **When `contract_version` does change,** the version in the title is
  updated, and the change is recorded in `CHANGELOG.md` so consumers pinning
  to a version can see what moved.
