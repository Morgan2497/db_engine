# Chapter 0605: Log + SSTable

## Overview

Chapter 0605 connects the pieces built in Chapters 0601–0604 into a small,
two-level storage engine:

```text
                         logical database
                                │
                  ┌───────────────┴───────────────┐
                  ▼                               ▼
        ┌──────────────────┐              ┌──────────────────┐
        │ MemTable         │              │ SSTable          │
        │ sorted, mutable  │              │ sorted, immutable│
        │ newest state     │              │ older state      │
        └─────────┬────────┘              └─────────┬────────┘
                 │ mirrored                         │ stored
                 ▼                                  ▼
        ┌──────────────────┐              ┌──────────────────┐
        │ append-only WAL  │              │ one disk file    │
        │ crash recovery   │              │ durable snapshot │
        └──────────────────┘              └──────────────────┘
```

Writes still append to the write-ahead log (WAL) and update the MemTable.
Reads now merge the MemTable with the SSTable, giving the newer MemTable
record priority. `KV.Compact()` periodically turns that merged view into a new
SSTable and resets the WAL and MemTable.

The main lesson is not merely how to create a sorted file. It is how to move
state safely between two durable representations without exposing stale data
or losing acknowledged writes.

> This directory begins from the Chapter 0604 snapshot. The declarations and
> flows below describe the Chapter 0605 target implementation.

---

## 1. Connection to Chapters 0601–0604

### 0601: Create an immutable sorted file

```text
sorted records ───► SSTable
```

### 0602: Query an SSTable

```text
SortedFile
├── Iter()
└── Seek(key)
```

### 0603: Give memory the same sorted interface

```text
SortedArray ──implements──► SortedKV
SortedFile  ──implements──► SortedKV
```

### 0604: Merge multiple sorted sources

```text
MemTable iterator ──┐
                     ├──► MergedSortedKVIter
SSTable iterator  ──┘
```

### 0605: Integrate their lifecycle

```text
write ──► WAL + MemTable
read  ──► merge(MemTable, SSTable) ──► hide tombstones
flush ──► merge ──► temporary SSTable ──► atomic replacement
```

---

## 2. Why Keep Both a WAL and an SSTable?

An append-only WAL makes foreground writes cheap and crash-recoverable. A
write appends one record and calls `fsync`; it does not rewrite a large disk
index in place.

The WAL is not a good query structure, however. It contains history rather
than only current state:

```text
SET a = 1
SET b = 2
SET a = 3
DEL b
```

The MemTable mirrors the WAL as the latest sorted state, making reads fast:

```text
a → 3
b → tombstone
```

Without compaction, both the WAL and MemTable grow forever. An SSTable solves
that problem by storing a compact, immutable snapshot. After the current
MemTable has been merged into that snapshot, the WAL and MemTable can be
reset.

The intended `KV` ownership becomes:

```go
type KV struct {
	log  Log
	mem  SortedArray
	main SortedFile
}
```

---

## 3. Reads Must Merge New and Old State

Suppose the current SSTable contains:

```text
a → old-A
b → old-B
c → old-C
```

and newer writes in the MemTable contain:

```text
b → new-B
d → new-D
```

The database must expose:

```text
a → old-A
b → new-B       MemTable wins
c → old-C
d → new-D
```

The level order carries priority:

```go
MergedSortedKV{&kv.mem, &kv.main}
```

Index `0` is newer than index `1`. When both iterators point to the same key,
the merged iterator returns the MemTable version and advances past both
physical copies before returning the next logical key.

### Point reads

`Get` can be expressed through the same merged seek used by range queries:

```text
Get(key)
  └── Seek(key)
       ├── MemTable.Seek(key)
       ├── SSTable.Seek(key)
       ├── choose the lowest key, preferring the MemTable on ties
       └── skip deleted records
```

### Range reads

`MergedSortedKV.Seek(key)` creates one positioned iterator per source:

```go
type SortedKV interface {
	EstimatedSize() int
	Iter() (SortedKVIter, error)
	Seek(key []byte) (SortedKVIter, error)
}
```

This lets `KV.Seek` and `KV.Range` continue to return one globally ordered
stream even though records live in two places.

---

## 4. Deletes Need Tombstones

Deleting a key from only the MemTable is incorrect when an older copy remains
in the SSTable:

```text
before delete                 after physically removing MemTable key

MemTable: a → new             MemTable: (no a)
SSTable:  a → old             SSTable:  a → old
                                      │
                                      └── stale value becomes visible
```

The upper level must retain evidence of the deletion:

```text
MemTable: a → TOMBSTONE
SSTable:  a → old

merged winner: MemTable tombstone ──► key is absent
```

For that reason, `SortedArray` gains a parallel deletion slice:

```go
type SortedArray struct {
	keys    [][]byte
	vals    [][]byte
	deleted []bool
}
```

Every operation must keep all three slices aligned:

```text
index       0          1          2
keys      ["a"]      ["b"]      ["c"]
vals      ["1"]      [nil]      ["3"]
deleted   [false]     [true]     [false]
```

This affects `Push`, `Pop`, `Clear`, `Set`, `Del`, `Iter`, and `Seek`.
Setting a tombstoned key revives it by storing the value and clearing the
flag. Deleting an SSTable-only key inserts a tombstone into the MemTable even
though the key was not previously present there.

The shared iterator interface also exposes deletion state:

```go
type SortedKVIter interface {
	Valid() bool
	Key() []byte
	Val() []byte
	Deleted() bool
	Next() error
	Prev() error
}
```

An SSTable in this two-level design always returns `false` from `Deleted()`.
Tombstones are discarded when compacting into the bottom level because there
is no older level left for them to hide.

---

## 5. Hide Tombstones at the Database Boundary

The merge layer must preserve tombstones so that a newer deletion can defeat
an older value. Callers of `KV`, however, must never see them as rows.

```text
raw merged iterator
      │
      ├── a → value
      ├── b → TOMBSTONE
      └── c → value
      │
      ▼
NoDeletedIter
      ├── a → value
      └── c → value
```

`filterDeleted` first advances past a tombstone at the initial seek position.
`NoDeletedIter.Next()` and `Prev()` then continue advancing until they reach a
visible record or the iterator becomes invalid.

The placement of this filter matters:

```text
correct:   merge versions ──► choose newest ──► hide winning tombstone
incorrect: hide tombstones in each level ──► merge
```

Filtering too early would remove the MemTable tombstone and reveal the stale
SSTable value it was meant to suppress.

---

## 6. Why `Size()` Becomes `EstimatedSize()`

The SSTable format starts with a key count followed by an offset table:

```text
[true key count][offset 0][offset 1] ... [record 0][record 1] ...
```

Before Chapter 0605, the input count was exact. During a merge it becomes only
an upper bound because duplicate keys collapse and tombstones disappear:

```text
MemTable entries: 3
SSTable entries:  4
estimated maximum: 7
actual output:      5
```

Therefore the interface calls the value `EstimatedSize`. The writer reserves
space using that upper bound, streams the merge once, counts the records it
actually writes, and finally stores the true count in the file header.

```text
[5][offsets for 5 records][unused reserved gap][5 encoded records]
```

On filesystems that support sparse files, the unwritten gap is a hole and does
not necessarily consume physical blocks. More importantly, over-allocation
allows the SSTable to be built in one pass without first materializing the
entire merged result in memory.

---

## 7. Compaction: Move WAL State into the SSTable

`KV.Compact()` has three logical stages:

```text
1. BUILD
   merge(MemTable, old SSTable)
                │
                ▼
        temporary SSTable + fsync

2. PUBLISH
   atomically rename temporary SSTable over the old SSTable
   fsync the parent directory

3. RESET
   clear MemTable
   seek WAL to offset 0 and truncate it
```

### Stage 1: Build a temporary file

The input order is important:

```go
m := MergedSortedKV{&kv.mem, &kv.main}
```

The MemTable is first, so its updates and tombstones win. The SSTable writer
skips tombstones, producing only the current live state.

The temporary file should be created in the same directory as the destination
SSTable so that replacement remains a same-filesystem rename.

### Stage 2: Publish atomically

Replacing the SSTable must not expose a half-written file. The safe sequence
is:

```text
write temporary file ──► fsync file ──► rename ──► fsync parent directory
```

On Unix, rename atomically switches the directory entry. Synchronizing the
parent directory makes that name change durable across a power failure.
Platform-specific behavior belongs in `os_unix.go` and `os_other.go`.

### Stage 3: Reset the upper level

Only after the new SSTable is safely published may compaction clear memory and
truncate the WAL:

```go
kv.mem.Clear()
return kv.log.Truncate()
```

`Log.Truncate()` seeks to the beginning before truncating the file to zero
bytes. Otherwise the next append could continue from an old file offset.

---

## 8. Crash-Safety Invariant

At every point, an acknowledged update must exist in at least one durable
place:

| Moment | WAL | Old SSTable | Temporary/new SSTable |
| --- | --- | --- | --- |
| Before compaction | current updates | older snapshot | absent |
| While building | current updates | older snapshot | incomplete |
| After temp `fsync` | current updates | older snapshot | complete |
| After rename | current updates | replaced | complete and published |
| After WAL truncate | empty | replaced | complete and published |

This ordering makes early failures recoverable:

- A failure while building leaves the old SSTable and WAL authoritative.
- A failure before publication leaves the temporary file disposable.
- A failure after publication but before WAL truncation may replay updates
  already present in the SSTable, but the newer MemTable copies win and the
  logical result remains correct.
- Clearing or truncating before publication would create a data-loss window
  and must never happen.

---

## 9. Open and Close Lifecycle

Opening the database now has two independent sources to initialize:

```text
KV.Open()
├── open WAL
│   └── replay entries into MemTable, preserving tombstones
└── open existing SSTable if present
```

WAL replay must preserve the latest deletion marker. Earlier chapters could
omit deleted entries after replay because memory was the whole database. That
is no longer safe: omitting the marker could reveal an older SSTable value.

If opening either resource fails, already-open resources must be closed.
Likewise, `KV.Close()` must close both the WAL and SSTable and handle an absent
SSTable cleanly.

---

## 10. End-to-End Examples

### Update an existing SSTable key

```text
SSTable: b → old

Set(b, new)
├── append SET b=new to WAL and fsync
└── MemTable: b → new

Get(b)
└── merged tie ──► MemTable wins ──► new

Compact()
└── new SSTable: b → new
```

### Delete an SSTable-only key

```text
SSTable: c → old
MemTable: (no c)

Del(c)
├── append DEL c to WAL and fsync
└── MemTable: c → TOMBSTONE

Get(c)
└── tombstone wins, then visibility filter reports absent

Compact()
└── c is omitted from the new bottom-level SSTable
```

### Delete and then recreate a key

```text
Del(c)       ──► MemTable: c → TOMBSTONE
Set(c, new)  ──► MemTable: c → new, deleted=false
```

The last operation in the WAL determines the current MemTable record after a
restart.

---

## 11. Implementation Checklist

### Sorted interfaces and arrays

- Rename `Size()` in `SortedKV` to `EstimatedSize()`.
- Add `Seek(key)` to `SortedKV`.
- Add `Deleted()` to `SortedKVIter`.
- Add and maintain `SortedArray.deleted` in every mutating operation.
- Preserve tombstones during WAL replay.

### Merge and query paths

- Implement `MergedSortedKV.Seek` by seeking every level.
- Propagate the winning level's deletion flag.
- Make `KV.Seek` merge MemTable first and SSTable second.
- Filter tombstones only after the merge.
- Verify both forward and reverse iteration skip deleted records.

### SSTable writing

- Add `SortedFile.Open` for an existing snapshot.
- Allocate offsets using `EstimatedSize()`.
- Skip tombstones and write the actual final key count.
- Synchronize the completed temporary file.

### Compaction and durability

- Build the replacement SSTable in the destination directory.
- Atomically rename it over the old file.
- Synchronize the parent directory on Unix.
- Reopen or replace the active `SortedFile` handle safely.
- Clear the MemTable and truncate the WAL only after publication.
- Remove abandoned temporary files on errors.

---

## 12. Tests Worth Adding

The chapter is complete when tests cover behavior, not only individual helper
methods:

1. A MemTable value overrides the same SSTable key.
2. A MemTable tombstone hides an SSTable key in `Get`, `Seek`, and `Range`.
3. Forward and reverse iteration both skip tombstones.
4. Setting a deleted key clears its tombstone.
5. Compaction removes duplicates and deleted keys from the output SSTable.
6. Compaction preserves keys found in only one of the two levels.
7. Closing and reopening after compaction returns the same logical data.
8. The WAL is empty after successful compaction.
9. A nonexistent SSTable is valid on first open.
10. Empty-input compaction creates a readable empty SSTable.

---

## 13. What This Chapter Does Not Solve Yet

This is a deliberately small two-level design:

```text
level 0: one WAL-backed MemTable
level 1: one SSTable
```

Every compaction rewrites the complete SSTable, so the design is not yet
efficient for a large database. Later LSM-tree chapters extend this idea to
multiple SSTable levels and control when and how much data is merged.

Chapter 0605 establishes the correctness rules those later designs depend on:

- newer levels override older levels;
- deletion markers remain until no older copy can reappear;
- queries merge all relevant levels;
- immutable files are published atomically; and
- durable upper-level state is discarded only after lower-level publication.

That is the bridge from separate WAL, sorted-file, and merge exercises to the
first complete log-and-SSTable storage lifecycle.
