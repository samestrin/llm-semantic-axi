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

Chunk, `file_hash`, `calibration_metadata`, `last_index`, and memory point IDs are
deterministic UUIDs derived from a string by SHA-256: `stringToUUID(s)` hashes
`s` with SHA-256 and formats bytes `0:4-4:6-6:8-8:10-10:16` as a UUID
(`storage_qdrant.go:24-29`). The exception is `retrieval_log` points, whose IDs
are time-based and not stable: `retrievalLogID` hashes
`"log:<unix-nanos>"` (`storage_qdrant_stats.go:24-28`).

## The six point kinds

One Qdrant collection holds six kinds of points. They are distinguished by
payload, not by collection or by ID namespace alone.

### 1. Chunk points (code chunks)

Written by `chunkToPoint` (`storage_qdrant.go:1436-1460`).

- **ID:** `stringToUUID(chunk.ID)` (`:1444`).
- **Vector:** the real embedding of the chunk content (`:1445`).
- **Payload** (`:1446-1458`):

| Key | Type | Written at |
| --- | --- | --- |
| `chunk_id` | string — original chunk ID | `:1447` |
| `file_path` | string | `:1448` |
| `type` | string — `ChunkType.String()`: the values emitted by the registered chunkers are `function`, `method`, `struct`, `interface`, `file`; the default branch also returns `unknown` for any out-of-range value (`internal/semantic/types.go:23-38`, default at `:36-37`) | `:1449` |
| `name` | string | `:1450` |
| `signature` | string | `:1451` |
| `content` | string — full chunk text | `:1452` |
| `start_line` | int | `:1453` |
| `end_line` | int | `:1454` |
| `language` | string | `:1455` |
| `domain` | string — defaults to `"code"` when unset (`:1437-1441`) | `:1456` |
| `content_hash` | string | `:1457` |

### 2. `file_hash` points (incremental-index bookkeeping)

Written by `SetFileHash` (`storage_qdrant.go:1403-1433`).

- **ID:** `stringToUUID("file_hash:" + filePath)` (`fileHashID`, `:1322-1324`;
  used at `:1415`).
- **Vector:** a zero vector of `embeddingDim` floats — a dummy, never
  semantically meaningful (`:1411-1412`, used at `:1416`).
- **Payload** (`:1417-1421`):

| Key | Type | Written at |
| --- | --- | --- |
| `type` | string — always `"file_hash"` | `:1418` |
| `file_path` | string | `:1419` |
| `content_hash` | string — hash of the file's content at index time | `:1420` |

### 3. The `calibration_metadata` point (singleton)

Written by `SetCalibrationMetadata` (`storage_qdrant.go:1590-1624`); restored
across collection clears at `:755-767`.

- **ID:** `stringToUUID("calibration_metadata")` (`calibrationMetadataID`,
  `:1545-1548`; used at `:1607`). Exactly one such point exists per collection.
- **Vector:** a zero vector of `embeddingDim` floats — a dummy (`:1603-1604`,
  used at `:1608`).
- **Payload** (`:1609-1612`):

| Key | Type | Written at |
| --- | --- | --- |
| `type` | string — always `"calibration_metadata"` | `:1610` |
| `calibration_json` | string — JSON-serialized `CalibrationMetadata` | `:1611` |

The JSON in `calibration_json` has these fields
(`internal/semantic/calibration.go:27-36`): `embedding_model` (string),
`calibration_date` (RFC 3339 timestamp), `perfect_match_score` (float),
`baseline_score` (float), `score_range` (float), `high_threshold` (float),
`medium_threshold` (float), `low_threshold` (float).

### 4. Memory entry points

Written by `memoryToPoint` (`storage_qdrant.go:912-932`).

- **ID:** `stringToUUID("memory:" + entry.ID)` (`memoryPointID`, `:907-909`;
  used at `:914`).
- **Vector:** a **real** embedding (`:915`) — memory entries are
  semantically searchable, unlike the zero-vector bookkeeping points.
- **Payload** (`:916-930`). Note: memory points carry **no `type` key**;
  their discriminator is `entry_type`.

Keys written at creation:

| Key | Type | Written at |
| --- | --- | --- |
| `memory_id` | string — original entry ID | `:917` |
| `question` | string | `:918` |
| `answer` | string | `:919` |
| `tags` | string — comma-joined | `:920` |
| `source` | string | `:921` |
| `status` | string — a `MemoryStatus`: `pending` or `promoted` (`internal/semantic/memory_entry.go:11-19`) | `:922` |
| `occurrences` | int | `:923` |
| `entry_type` | string — always `"memory"` | `:924` |
| `created_at` | string | `:925` |
| `updated_at` | string | `:926` |
| `file_path` | string | `:927` |
| `sprints` | string — comma-joined | `:928` |
| `files` | string — comma-joined | `:929` |

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

### 6. The `last_index` point (singleton)

The commit and time of the last index that brought the whole collection up to
that commit. Written by `SetLastIndex` (`storage_qdrant.go:1665-1698`, payload
built by `setLastIndexLocked` at `:1677-1698`); read by `getLastIndexLocked`
(`:1643-1663`), which `Stats` uses to report `last_updated` (`:676-691`);
removed by a collection clear (`Clear`, `:698`), which unlike
`calibration_metadata` does not restore it: a re-index that stops after the
clear must leave no record, so it reads as unknown rather than current, as the
sqlite backend does. Added after version 1 was published; it is additive, so
the version is unchanged.

- **ID:** `stringToUUID("last_index")` (`lastIndexID`, `:1626-1629`; used at
  `:1682`). At most one such point exists per collection; it is absent until
  the first index is recorded.
- **Vector:** a zero vector of `embeddingDim` floats — a dummy (`:1679`, used
  at `:1683`).
- **Payload** (`:1684-1688`):

| Key | Type | Written at |
| --- | --- | --- |
| `type` | string — always `"last_index"` | `:1685` |
| `commit` | string — the full commit hash of `HEAD` when the index started; empty when the indexed directory is not in a git repository | `:1686` |
| `indexed_at` | string — RFC 3339 timestamp in UTC, with fractional seconds, of when the index started | `:1687` |

## Filter rule: how to select chunks

**Chunks are exactly the points that carry a `chunk_id` payload key.** Only
`chunkToPoint` writes `chunk_id` (`:1447`); none of the other five kinds do.
The read path relies on the same key: `payloadToChunk` prefers `chunk_id` from
the payload over the point UUID (`:1470-1472`).

**`type` is not chunk-exclusive.** `file_hash` points, the
`calibration_metadata` point and the `last_index` point also carry a `type`
key (`:1418`, `:1610`, `:1685`), while
memory points carry none at all. Filtering on the presence of `type`, or
treating every `type`-bearing point as a chunk, will misclassify points. To
select:

- chunks → points having `chunk_id`;
- file-hash bookkeeping → `type == "file_hash"` (this is how
  `ListIndexedFiles` selects them, `:1336-1340`);
- calibration metadata → `type == "calibration_metadata"` (read by ID at
  `:1562`);
- the last index record → `type == "last_index"` (read by ID at `:1647`);
- memory entries → `entry_type == "memory"` (this is how memory search
  filters, `:1071-1073` and `:1230-1232`; also the decode guard at `:941`);
- retrieval logs → `entry_type == "retrieval_log"`
  (`storage_qdrant_stats.go:103`).

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
