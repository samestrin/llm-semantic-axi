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

Written by `chunkToPoint` (`storage_qdrant.go:1442-1466`).

- **ID:** `stringToUUID(chunk.ID)` (`:1450`).
- **Vector:** the real embedding of the chunk content (`:1451`).
- **Payload** (`:1452-1464`):

| Key | Type | Written at |
| --- | --- | --- |
| `chunk_id` | string — original chunk ID | `:1453` |
| `file_path` | string | `:1454` |
| `type` | string — `ChunkType.String()`: the values emitted by the registered chunkers are `function`, `method`, `struct`, `interface`, `file`; the default branch also returns `unknown` for any out-of-range value (`internal/semantic/types.go:23-38`, default at `:36-37`) | `:1455` |
| `name` | string | `:1456` |
| `signature` | string | `:1457` |
| `content` | string — full chunk text | `:1458` |
| `start_line` | int | `:1459` |
| `end_line` | int | `:1460` |
| `language` | string | `:1461` |
| `domain` | string — defaults to `"code"` when unset (`:1443-1447`) | `:1462` |
| `content_hash` | string | `:1463` |

### 2. `file_hash` points (incremental-index bookkeeping)

Written by `SetFileHash` (`storage_qdrant.go:1409-1439`).

- **ID:** `stringToUUID("file_hash:" + filePath)` (`fileHashID`, `:1328-1330`;
  used at `:1421`).
- **Vector:** a zero vector of `embeddingDim` floats — a dummy, never
  semantically meaningful (`:1417-1418`, used at `:1422`).
- **Payload** (`:1423-1427`):

| Key | Type | Written at |
| --- | --- | --- |
| `type` | string — always `"file_hash"` | `:1424` |
| `file_path` | string | `:1425` |
| `content_hash` | string — hash of the file's content at index time | `:1426` |

### 3. The `calibration_metadata` point (singleton)

Written by `SetCalibrationMetadata` (`storage_qdrant.go:1596-1630`); restored
across collection clears at `:761-773`.

- **ID:** `stringToUUID("calibration_metadata")` (`calibrationMetadataID`,
  `:1551-1554`; used at `:1613`). Exactly one such point exists per collection.
- **Vector:** a zero vector of `embeddingDim` floats — a dummy (`:1609-1610`,
  used at `:1614`).
- **Payload** (`:1615-1618`):

| Key | Type | Written at |
| --- | --- | --- |
| `type` | string — always `"calibration_metadata"` | `:1616` |
| `calibration_json` | string — JSON-serialized `CalibrationMetadata` | `:1617` |

The JSON in `calibration_json` has these fields
(`internal/semantic/calibration.go:27-36`): `embedding_model` (string),
`calibration_date` (RFC 3339 timestamp), `perfect_match_score` (float),
`baseline_score` (float), `score_range` (float), `high_threshold` (float),
`medium_threshold` (float), `low_threshold` (float).

### 4. Memory entry points

Written by `memoryToPoint` (`storage_qdrant.go:918-938`).

- **ID:** `stringToUUID("memory:" + entry.ID)` (`memoryPointID`, `:913-915`;
  used at `:920`).
- **Vector:** a **real** embedding (`:921`) — memory entries are
  semantically searchable, unlike the zero-vector bookkeeping points.
- **Payload** (`:922-936`). Note: memory points carry **no `type` key**;
  their discriminator is `entry_type`.

Keys written at creation:

| Key | Type | Written at |
| --- | --- | --- |
| `memory_id` | string — original entry ID | `:923` |
| `question` | string | `:924` |
| `answer` | string | `:925` |
| `tags` | string — comma-joined | `:926` |
| `source` | string | `:927` |
| `status` | string — a `MemoryStatus`: `pending` or `promoted` (`internal/semantic/memory_entry.go:11-19`) | `:928` |
| `occurrences` | int | `:929` |
| `entry_type` | string — always `"memory"` | `:930` |
| `created_at` | string | `:931` |
| `updated_at` | string | `:932` |
| `file_path` | string | `:933` |
| `sprints` | string — comma-joined | `:934` |
| `files` | string — comma-joined | `:935` |

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
that commit. Written by `SetLastIndex` (`storage_qdrant.go:1671-1704`, payload
built by `setLastIndexLocked` at `:1683-1704`); read by `getLastIndexLocked`
(`:1649-1669`), which `Stats` uses to report `last_updated` (`:684-692`);
removed by a collection clear (`Clear`, `:704`), which unlike
`calibration_metadata` does not restore it: a re-index that stops after the
clear must leave no record, so it reads as unknown rather than current, as the
sqlite backend does. Added after version 1 was published; it is additive, so
the version is unchanged.

- **ID:** `stringToUUID("last_index")` (`lastIndexID`, `:1632-1635`; used at
  `:1688`). At most one such point exists per collection; it is absent until
  the first index is recorded.
- **Vector:** a zero vector of `embeddingDim` floats — a dummy (`:1685`, used
  at `:1689`).
- **Payload** (`:1690-1694`):

| Key | Type | Written at |
| --- | --- | --- |
| `type` | string — always `"last_index"` | `:1691` |
| `commit` | string — the full commit hash of `HEAD` when the index started; empty when the indexed directory is not in a git repository | `:1692` |
| `indexed_at` | string — RFC 3339 timestamp in UTC, with fractional seconds, of when the index started | `:1693` |

## Filter rule: how to select chunks

**Chunks are exactly the points that carry a `chunk_id` payload key.** Only
`chunkToPoint` writes `chunk_id` (`:1453`); none of the other five kinds do.
The read path relies on the same key: `payloadToChunk` prefers `chunk_id` from
the payload over the point UUID (`:1476-1478`).

**`type` is not chunk-exclusive.** `file_hash` points, the
`calibration_metadata` point and the `last_index` point also carry a `type`
key (`:1424`, `:1616`, `:1691`), while
memory points carry none at all. Filtering on the presence of `type`, or
treating every `type`-bearing point as a chunk, will misclassify points. To
select:

- chunks → points having `chunk_id` (this is how `Stats` counts them for
  `chunks_total`, `:673-679`);
- file-hash bookkeeping → `type == "file_hash"` (this is how
  `ListIndexedFiles` selects them, `:1342-1346`);
- calibration metadata → `type == "calibration_metadata"` (read by ID at
  `:1568`);
- the last index record → `type == "last_index"` (read by ID at `:1653`);
- memory entries → `entry_type == "memory"` (this is how memory search
  filters, `:1077-1079` and `:1236-1238`; also the decode guard at `:947`);
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
