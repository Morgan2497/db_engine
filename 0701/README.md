# Step 0701 Evaluation: Atomic Store

## Verdict

This directory is a working, well-tested continuation of the database built
through Step 0605, but it does **not yet implement the main requirement of
Step 0701** from *How to Code a Database in 45 Steps with Go*.

The book's Step 0701 introduces an atomically updated metadata store for future
LSM-tree levels. The required `KVMetaStore`, `KVMetaItem`, and `KVMetaData`
types are not present here. The current `KV.Compact` still replaces one
fixed-name SSTable with `rename`, which is the Step 0605 design.

Current status: **good foundation; Step 0701 incomplete**.

## What the Book Requires in Step 0701

An LSM tree eventually owns multiple SSTable files, so their names cannot all
be fixed in advance. The database needs durable metadata that says which
SSTable file is current. Updating that metadata must survive a crash without
leaving the database unable to identify a valid state.

The chapter solves this with double buffering:

```text
metadata slot 0: version 124 | serialized data | checksum
metadata slot 1: version 123 | serialized data | checksum
                         ^
                         newest valid slot wins
```

The target API described by the book is:

```go
type KVMetaStore struct {
	slots [2]KVMetaItem
}

type KVMetaItem struct {
	FileName string
	fp       *os.File
	data     KVMetaData
}

type KVMetaData struct {
	Version uint64
	SSTable string
}

func (meta *KVMetaStore) Open() error
func (meta *KVMetaStore) Close() error
func (meta *KVMetaStore) Get() KVMetaData
func (meta *KVMetaStore) Set(data KVMetaData) error
```

Required behavior:

- `Open` reads both slots and ignores an invalid copy, including malformed
  serialized data and a checksum mismatch.
- `Get` returns the valid copy with the greatest version.
- `Set` overwrites the slot with the smaller version.
- `Set` calls `fsync` so a successful update is durable.
- Tests demonstrate recovery when either copy is corrupt or incomplete.

This metadata layer is intentionally separate from wiring it into `KV`; the
directory layout and dynamic SSTable integration are the subject of Step 0702.

## What Is Implemented Here

The package currently contains a much broader database implementation:

```text
SQL text
   |
   v
parser -> expression evaluation -> table/row encoding
                                      |
                                      v
                    KV logical view (MemTable + SSTable)
                         |                    |
                    append-only WAL     sorted disk file
```

Implemented capabilities include:

- a checksummed, append-only write-ahead log with `fsync` on writes;
- recovery that ignores a truncated or bad-checksum final WAL record;
- an in-memory sorted MemTable;
- immutable sorted-file creation, iteration, seek, and binary search;
- merging of newer and older sorted levels with tombstone handling;
- point reads, forward and reverse ranges, update modes, and compaction;
- typed cells, rows, schemas, primary and composite keys;
- SQL parsing and execution for create, insert, select, update, and delete;
- expression evaluation and primary-key range matching;
- a standalone Bloom filter implementation.

The existing compaction lifecycle is:

```text
MemTable + current SSTable
            |
            v
      temporary SSTable
            |
            v
 atomic rename over one fixed SSTable
            |
            v
 clear MemTable and truncate WAL
```

That lifecycle is valuable and appropriate for Step 0605. Step 0701 adds the
durable metadata primitive needed to move beyond a single fixed SSTable.

## Evaluation Against Step 0701

| Criterion | Status | Evidence |
|---|---:|---|
| Two metadata slots | Missing | No `KVMetaStore` or `[2]KVMetaItem` exists |
| Monotonic metadata version | Missing | No `KVMetaData.Version` exists |
| SSTable filename in metadata | Missing | `kv.main.FileName` is configured directly |
| Serialized metadata with checksum | Missing | Checksums exist for WAL entries only |
| Ignore one damaged metadata copy | Missing | No metadata open/recovery path exists |
| Select newest valid copy | Missing | No metadata `Get` exists |
| Overwrite older slot and `fsync` | Missing | No metadata `Set` exists |
| Existing WAL durability | Pass | `Log.Write` writes then calls `Sync` |
| Existing single-SSTable atomic replacement | Pass | `Compact` uses a temp file and `renameSync` |
| Existing package tests | Pass | `go test ./0701` succeeds |
| Race detector | Pass | `go test -race ./0701` succeeds |
| Static analysis | Pass | `go vet ./0701` succeeds |

## Strengths

- Durability-related ordering is mostly clear: WAL writes are synced before
  MemTable mutation, and the compacted file is synced before replacement.
- `renameSync` also syncs the containing directory on Unix, which is an
  important detail for durable filename changes.
- The merged iterator correctly gives the earlier (newer) level priority on
  duplicate keys and hides tombstones from public reads.
- Tests cover SQL behavior, key ordering, range bounds, reverse iteration,
  recovery, merge behavior, sorted files, serialization, and Bloom filters.
- The current package has 58 named top-level tests and passes both the race
  detector and `go vet`.

## Gaps and Risks

1. **The chapter objective is absent.** Passing tests validate the inherited
   database features, not the Step 0701 atomic metadata store.
2. **There are no metadata fault-injection tests.** The critical cases are a
   truncated slot, bad checksum, malformed payload, one missing slot, and
   differing valid versions.
3. **The database still depends on a fixed SSTable path.** This prevents the
   later multi-file/multi-level LSM design from recording its active files.
4. **The Bloom filter is isolated.** It is tested, but it is not persisted in
   or consulted by `SortedFile`; it should not be described as a query
   optimization of the current engine yet.
5. **The public construction story is unfinished.** File paths are stored in
   unexported `KV` fields and there is no constructor or options object. Step
   0702's directory-based options will naturally address this.

## Recommended Next Implementation

Implement Step 0701 as a focused metadata component before changing `KV`:

1. Define `KVMetaData`, `KVMetaItem`, and `KVMetaStore` in a new
   `kv_meta.go`.
2. Choose a deterministic record format containing payload length, serialized
   metadata, and CRC32. JSON is sufficient for the payload.
3. Make each slot reader validate framing, deserialization, and checksum; an
   invalid slot should be treated as unavailable rather than fatal when the
   other slot is usable.
4. Have `Open` load both slot states and `Get` select the greatest valid
   version.
5. Have `Set` write the lower-version slot, truncate stale bytes, and call
   `Sync` before updating the in-memory slot state.
6. Add table-driven recovery tests for damage to slot 0, damage to slot 1,
   both valid with different versions, and repeated alternating updates.

Keep Step 0702 concerns out of that first change: dynamic SSTable names,
directory ownership, and connecting metadata to compaction belong to the next
chapter.

## Verification

Run from the module root:

```sh
cd /home/morgankim/synapse/db_engine
go test ./0701
go test -race ./0701
go vet ./0701
```

Evaluation performed against pages 74-76 of
`/home/morgankim/Documents/ebooks/db_in_45_steps_go.pdf`, especially the
Step 0701 requirements on page 76.
