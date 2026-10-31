# Storage Contract — Version 1

This document is the external contract for the data `llm-semantic` writes into a
Qdrant collection. External consumers (such as `atcr`) may rely on the point
kinds, payload keys, and value shapes described here. Every key and shape is
cited to the line of code at `main` that writes it, in
`internal/semantic/storage_qdrant.go` unless noted otherwise.

**Scope of version 1:** this contract covers the **Qdrant store only**
(the collection selected by `--collection`, default `llm_semantic`). The
default storage backend is sqlite (`--storage sqlite`,
`internal/semantic/commands/root.go:130`), whose on-disk file is
`.index/semantic.db` (`internal/semantic/storage_sqlite.go:1421`). The sqlite
file is an internal implementation detail and is **not** part of this contract.

## Point ID derivation

Chunk, `file_hash`, `calibration_metadata`, and memory point IDs are
deterministic UUIDs derived from a string by SHA-256: `stringToUUID(s)` hashes
`s` with SHA-256 and formats bytes `0:4-4:6-6:8-8:10-10:16` as a UUID
(`storage_qdrant.go:24-29`). The exception is `retrieval_log` points, whose IDs
are time-based and not stable: `retrievalLogID` hashes
`"log:<unix-nanos>"` (`storage_qdrant_stats.go:24-28`).

## The five point kinds

One Qdrant collection holds five kinds of points. They are distinguished by
payload, not by collection or by ID namespace alone.

### 1. Chunk points (code chunks)

Written by `chunkToPoint` (`storage_qdrant.go:1423-1447`).

- **ID:** `stringToUUID(chunk.ID)` (`:1431`).
- **Vector:** the real embedding of the chunk content (`:1432`).
- **Payload** (`:1433-1445`):

| Key | Type | Written at |
| --- | --- | --- |
| `chunk_id` | string — original chunk ID | `:1434` |
| `file_path` | string | `:1435` |
| `type` | string — `ChunkType.String()`: the values emitted by the registered chunkers are `function`, `method`, `struct`, `interface`, `file`; the default branch also returns `unknown` for any out-of-range value (`internal/semantic/types.go:23-38`, default at `:36-37`) | `:1436` |
| `name` | string | `:1437` |
| `signature` | string | `:1438` |
| `content` | string — full chunk text | `:1439` |
| `start_line` | int | `:1440` |
| `end_line` | int | `:1441` |
| `language` | string | `:1442` |
| `domain` | string — defaults to `"code"` when unset (`:1424-1428`) | `:1443` |
| `content_hash` | string | `:1444` |

### 2. `file_hash` points (incremental-index bookkeeping)

Written by `SetFileHash` (`storage_qdrant.go:1390-1420`).

- **ID:** `stringToUUID("file_hash:" + filePath)` (`fileHashID`, `:1309-1311`;
  used at `:1402`).
- **Vector:** a zero vector of `embeddingDim` floats — a dummy, never
  semantically meaningful (`:1398-1399`, used at `:1403`).
- **Payload** (`:1404-1408`):

| Key | Type | Written at |
| --- | --- | --- |
| `type` | string — always `"file_hash"` | `:1405` |
| `file_path` | string | `:1406` |
| `content_hash` | string — hash of the file's content at index time | `:1407` |

### 3. The `calibration_metadata` point (singleton)

Written by `SetCalibrationMetadata` (`storage_qdrant.go:1577-1611`); restored
across collection clears at `:742-754`.

- **ID:** `stringToUUID("calibration_metadata")` (`calibrationMetadataID`,
  `:1532-1535`; used at `:1594`). Exactly one such point exists per collection.
- **Vector:** a zero vector of `embeddingDim` floats — a dummy (`:1590-1591`,
  used at `:1595`).
- **Payload** (`:1596-1599`):

| Key | Type | Written at |
| --- | --- | --- |
| `type` | string — always `"calibration_metadata"` | `:1597` |
| `calibration_json` | string — JSON-serialized `CalibrationMetadata` | `:1598` |

The JSON in `calibration_json` has these fields
(`internal/semantic/calibration.go:27-36`): `embedding_model` (string),
`calibration_date` (RFC 3339 timestamp), `perfect_match_score` (float),
`baseline_score` (float), `score_range` (float), `high_threshold` (float),
`medium_threshold` (float), `low_threshold` (float).

### 4. Memory entry points

Written by `memoryToPoint` (`storage_qdrant.go:899-919`).

- **ID:** `stringToUUID("memory:" + entry.ID)` (`memoryPointID`, `:894-896`;
  used at `:901`).
- **Vector:** a **real** embedding (`:902`) — memory entries are
  semantically searchable, unlike the zero-vector bookkeeping points.
- **Payload** (`:903-917`). Note: memory points carry **no `type` key**;
  their discriminator is `entry_type`.

Keys written at creation:

| Key | Type | Written at |
| --- | --- | --- |
| `memory_id` | string — original entry ID | `:904` |
| `question` | string | `:905` |
| `answer` | string | `:906` |
| `tags` | string — comma-joined | `:907` |
| `source` | string | `:908` |
| `status` | string — a `MemoryStatus`: `pending` or `promoted` (`internal/semantic/memory_entry.go:11-19`) | `:909` |
| `occurrences` | int | `:910` |
| `entry_type` | string — always `"memory"` | `:911` |
| `created_at` | string | `:912` |
| `updated_at` | string | `:913` |
| `file_path` | string | `:914` |
| `sprints` | string — comma-joined | `:915` |
| `files` | string — comma-joined | `:916` |

Keys added or rewritten later (all in
`internal/semantic/storage_qdrant_stats.go`):

| Key | Type | Written at |
| --- | --- | --- |
| `retrieval_count` | int — number of tracked retrievals; absent until the first one | `TrackMemoryRetrieval`, `:74-75` (set via `SetPayload` at `:80-90`) |
| `avg_score` | double — running mean of retrieval scores; absent until the first tracked retrieval | `:76` (set via `SetPayload` at `:80-90`) |
| `last_retrieved` | string — RFC 3339 timestamp; absent until the first tracked retrieval | `:77` (set via `SetPayload` at `:80-90`) |
| `status` | string — rewritten by `UpdateMemoryStatsStatus` | `:446-451` |

### 5. `retrieval_log` points (memory retrieval events)

Written by `TrackMemoryRetrieval` (`storage_qdrant_stats.go:99-109`) into the
same collection, once per tracked memory retrieval;
`PruneMemoryRetrievalLog` scrolls and deletes them
(`storage_qdrant_stats.go:334-353`).

- **ID:** time-based, not deterministic — `stringToUUID("log:<unix-nanos>")`
  (`retrievalLogID`, `:24-28`; used at `:100`).
- **Vector:** a zero vector of `embeddingDim` floats — a dummy (`:97`,
  used at `:101`).
- **Payload** (`:102-108`):

| Key | Type | Written at |
| --- | --- | --- |
| `entry_type` | string — always `"retrieval_log"` | `:103` |
| `memory_id` | string — the retrieved memory's original ID | `:104` |
| `query` | string | `:105` |
| `score` | double — retrieval score | `:106` |
| `timestamp` | string — RFC 3339 | `:107` |

Note the shared `memory_id` key: a `retrieval_log` point is not a memory
entry. Select memory entries by `entry_type == "memory"`, not by the presence
of `memory_id`.

## Filter rule: how to select chunks

**Chunks are exactly the points that carry a `chunk_id` payload key.** Only
`chunkToPoint` writes `chunk_id` (`:1434`); none of the other four kinds do.
The read path relies on the same key: `payloadToChunk` prefers `chunk_id` from
the payload over the point UUID (`:1457-1459`).

**`type` is not chunk-exclusive.** `file_hash` points and the
`calibration_metadata` point also carry a `type` key (`:1405`, `:1597`), while
memory points carry none at all. Filtering on the presence of `type`, or
treating every `type`-bearing point as a chunk, will misclassify points. To
select:

- chunks → points having `chunk_id`;
- file-hash bookkeeping → `type == "file_hash"` (this is how
  `ListIndexedFiles` selects them, `:1323-1327`);
- calibration metadata → `type == "calibration_metadata"` (read by ID at
  `:1549`);
- memory entries → `entry_type == "memory"` (this is how memory search
  filters, `:1058-1060` and `:1217-1219`; also the decode guard at `:928`);
- retrieval logs → `entry_type == "retrieval_log"`
  (`storage_qdrant_stats.go:103`).

Consumers doing vector search over chunk points should also be aware that
zero-vector points (`file_hash`, `calibration_metadata`, `retrieval_log`)
exist in the same collection; the `chunk_id` presence rule is the reliable
way to exclude them.
