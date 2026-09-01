# Chapter 0700: LSM-Tree Introduction

## Overview

Chapter 0605 produced a correct but impractical two-level database:

```text
new writes                              older durable state
┌──────────────────┐                ┌──────────────────┐
│ WAL + MemTable   │                │ one SSTable      │
│ mutable, newest  │                │ immutable, older │
└──────────────────┘                └──────────────────┘
          │                                  │
          └─────────────────┼── merged for reads
                                             and compaction
```

Every `Compact()` merges the whole MemTable with the whole SSTable and replaces
the SSTable. That is correct, but a tiny batch of updates can rewrite the
entire database.

Chapter 0700 introduces the principle that fixes this scaling problem:

> Replace one repeatedly rebuilt structure with several size-bounded levels.
> Send writes into a small upper level first, then merge levels only when their
> size thresholds are reached.

An LSM-tree is not one particular tree. It is a policy for arranging multiple
data structures by age and moving data between them through merges.

This chapter introduces the design only. It adds no implementation files; the
following 07xx chapters build metadata storage, multiple SSTables, level
merging, and automatic compaction.

---

## 1. What an LSM-Tree Is

LSM stands for Log-Structured Merge, but the essential idea is broader than
either logs or trees:

```text
updates enter a small upper level
                 │
                 ▼
upper level reaches its limit
                 │
                 ▼
merge into a larger lower level
                 │
                 ▼
repeat as the database grows
```

Each level may use any representation that supports an efficient merge. In
this project:

```text
level 0  WAL-backed sorted MemTable
level 1  immutable sorted file
level 2  larger immutable sorted file
level 3  still larger immutable sorted file
...
```

The word "tree" describes neither the on-disk format nor a required pointer
structure. An LSM-tree is a collection of ordered levels plus rules for:

1. Where new writes enter.
2. Which version wins during reads.
3. When adjacent levels merge.
4. How level capacity grows.
5. When tombstones can finally be discarded.

---

## 2. From Two Levels to Many Levels

Chapter 0605 has:

```text
level 0: MemTable              recent and small
level 1: one SSTable           entire older database
```

Suppose the SSTable holds one million keys and the MemTable holds one thousand
updates. Chapter 0605 compaction performs roughly:

```text
1,000 MemTable records
       +
1,000,000 SSTable records
       │
       ▼
rewrite a new ~1,000,000-record SSTable
```

Repeating that operation for every small batch creates excessive write work.

The multi-level design instead uses geometrically growing capacities:

```text
level 0 capacity:       n
level 1 capacity:      2n
level 2 capacity:      4n
level 3 capacity:      8n
level 4 capacity:     16n
```

With a larger production growth factor, the sequence might be:

```text
n, 10n, 100n, 1000n, ...
```

Small batches merge into small levels frequently. Large levels are rewritten
much less frequently.

---

## 3. Upper Levels Are Newer

Level order carries version priority:

```text
level 0  newest
level 1
level 2
...
level k  oldest
```

Example:

```text
level 0:  x → 1, z → 3
level 1:  a → 8, y → 2, z → TOMBSTONE
level 2:  a → 0, b → 2, c → 3, z → 456
```

The logical view is:

```text
a → 8             level 1 beats level 2
b → 2
c → 3
x → 1
y → 2
z → 3             level 0 beats the lower tombstone and value
```

For a duplicated key, the first occurrence found from top to bottom is the
authoritative version.

This is the same priority rule used in Chapter 0604:

```go
MergedSortedKV{
	level0, // highest priority
	level1,
	level2, // lowest priority
}
```

---

## 4. Point Reads Search Top to Bottom

For an exact lookup:

```text
GET a
  │
  ├── search level 0
  │     ├── value found      → return it
  │     ├── tombstone found  → report absent
  │     └── no record        → continue
  ├── search level 1
  ├── search level 2
  └── continue toward the oldest level
```

Stopping at the first match preserves version priority:

```text
level 0: no a
level 1: a → 8       first match
level 2: a → 0       stale; do not use

result:  a → 8
```

---

## 5. Range Reads Use a k-Way Merge

A range query needs globally sorted output from every level:

```text
level 0: b, f
level 1: a, d, f
level 2: a, c, e, g
```

Reading one complete level at a time would produce unsorted duplicates:

```text
b, f, a, d, f, a, c, e, g       wrong
```

Instead, create one cursor per level and repeatedly choose the lowest current
key:

```text
level 0 iterator ──┐
level 1 iterator ──┼──► k-way merged iterator ──► a, b, c, d, e, f, g
level 2 iterator ──┘
```

When several iterators have the same key, the upper-level iterator wins and
all physical copies are advanced past that key.

Chapter 0604's `MergedSortedKV` is already a general k-way merge. Its slice of
levels supports any number of sources:

```go
MergedSortedKV{level0, level1, level2, level3}
```

---

## 6. Tombstones Must Travel Downward

Suppose an old level contains:

```text
level 3: customer:42 → Alice
```

A delete enters level 0:

```text
level 0: customer:42 → TOMBSTONE
level 3: customer:42 → Alice
```

If the tombstone were discarded immediately, the old value would become
visible again. It must survive while a lower level may hold an older version:

```text
level 0 full
   │ merge
   ▼
level 1: customer:42 → TOMBSTONE
   │ later merge
   ▼
level 2: customer:42 → TOMBSTONE
```

Only when compaction reaches the bottom and removes the last old value can
both records disappear:

```text
bottom-level merge input:
TOMBSTONE + Alice
        │
        ▼
output: no customer:42 record
```

This makes the bottom level special:

```text
intermediate levels  may need tombstones
bottom level         can discard resolved tombstones
```

Chapter 0605 could omit tombstones from its one SSTable precisely because that
SSTable was already the bottom level.

---

## 7. Merge Instead of In-Place Update

Inserting into the middle of a flat sorted array requires shifting later
elements:

```text
before inserting c:
[a][b][d][e]

make a gap:
[a][b][ ][d][e]
       ▲

after:
[a][b][c][d][e]
```

For an array of size `N`, an arbitrary insertion or deletion moves `O(N)`
elements in the worst case and about half the array on average.

Immutable sorted files avoid in-place editing:

```text
small sorted updates ──┐
                       ├──► linear merge ──► new immutable file
old sorted file ──────┘
```

The merge still costs linear time, but batching amortizes that cost over many
writes. Geometrically growing levels prevent a record from being merged into
the largest structure after every update.

---

## 8. How Levels Grow

Use a simplified capacity of one record at the top. Each equal-size collision
merges upward like carrying a binary digit.

```text
set a=2
[a=2]                                      sizes: 1

set c=3
[c=3] [a=2]                                sizes: 1, 1

merge equal sizes
[a=2, c=3]                                 sizes: 2

set d=4, then e=5
[e=5] [d=4] [a=2, c=3]                    sizes: 1, 1, 2

merge 1 + 1
[d=4, e=5] [a=2, c=3]                     sizes: 2, 2

merge 2 + 2
[a=2, c=3, d=4, e=5]                      sizes: 4
```

This resembles binary counting:

```text
record count    occupied capacities
1               1
2               2
3               1 + 2
4               4
5               1 + 4
6               2 + 4
7               1 + 2 + 4
8               8
```

At most one run of each capacity exists in this simplified model.

---

## 9. Updating a Key Across Levels

Suppose an older level contains:

```text
level 2:
a → 2
c → 3
d → 4
e → 5
```

Now write `SET a=0`. The engine does not modify level 2; it writes the new
version to level 0:

```text
level 0: a → 0
level 2: a → 2, c → 3, d → 4, e → 5
```

Queries return `a=0` because level 0 is newer. During a future merge:

```text
a → 0 from upper level ──┐
                            ├──► output a → 0
a → 2 from lower level ──┘    discard stale a → 2
```

The system replaces updates with new versions plus eventual merging.

---

## 10. Why the Number of Levels Is Logarithmic

With capacities:

```text
n, 2n, 4n, 8n, ..., 2^k n
```

the bottom capacity after `k` growth steps is `2^k n`. For total data size
`N`:

```text
2^k n ≈ N
k ≈ log₂(N/n)
```

Therefore the number of levels grows logarithmically rather than linearly.

| Records | Largest capacity | Approximate level count |
| ---: | ---: | ---: |
| 1 | 1 | 1 |
| 8 | 8 | 4 |
| 1,024 | 1,024 | 11 |
| 1,048,576 | 1,048,576 | 21 |

Doubling the database adds roughly one level; it does not double the number
of levels.

---

## 11. Cost Model

### Writes

A record starts at the top and may be rewritten once per level as it moves
downward:

```text
level 0 ──► level 1 ──► level 2 ──► ... ──► bottom
```

With `O(log N)` levels, the simplified model gives `O(log N)` amortized merge
work per inserted record, ignoring constants such as the growth factor and
bytes per value.

### Point reads

Each sorted-array level supports binary search, and a lookup checks levels
from newest to oldest until it finds a value or tombstone.

The chapter summarizes lookup as logarithmic. More precisely, independently
binary-searching geometrically sized levels has the worst-case sum:

```text
log 1 + log 2 + log 4 + ... + log N = O((log N)²)
```

Real LSM engines reduce practical read cost with Bloom filters, indexes,
caching, and level organization. Successful lookups may also stop early.

### Range reads

Range reads initialize one cursor per relevant level and perform a k-way
merge. Their cost depends on the number of returned records plus stale and
deleted versions that must be skipped.

---

## 12. Why the Top Level Uses a WAL and MemTable

Immutable files are ideal for merging, but creating a new file for every write
would be expensive. The top level is therefore directly writable:

```text
incoming SET/DEL
      │
      ├──► WAL       durable chronological operations
      └──► MemTable  queryable latest state
```

When the top-level threshold is reached:

```text
WAL + MemTable
      │
      ▼
new immutable SSTable run
      │
      ▼
reset WAL and MemTable
```

| Component | Role |
| --- | --- |
| WAL | Make recent writes recoverable |
| MemTable | Make recent writes efficiently queryable |
| SSTable levels | Store immutable sorted runs at increasing scales |
| Merge policy | Move and deduplicate versions between levels |

---

## 13. Arrays, Trees, and Files

The LSM principle does not require each level to be a flat array. A level only
needs a representation that supports the selected lookup and merge behavior.

Possible representations include:

```text
sorted flat array
static B-tree/B+tree
sorted string table with a page index
hash-based structure when ordering is unnecessary
```

This project begins with arrays because immutable-file construction and linear
merging are easy to see.

For real disk access, a page-oriented n-ary index is more efficient than many
tiny binary-search reads. Storage is transferred in blocks, so indexes should
use each fetched page effectively. A static B-tree-style index can provide
that organization without requiring mutable B-tree update logic.

An LSM-tree may therefore contain tree-shaped indexes, but those indexes are
components inside the LSM design rather than the definition of LSM itself.

---

## 14. LSM-Tree Versus B+Tree

| Property | LSM-style design | Mutable B+tree design |
| --- | --- | --- |
| New writes | Enter an upper level | Modify target pages |
| Existing disk state | Usually immutable | Updated in place or copy-on-write |
| Reorganization | Sequential merges | Page splits, merges, and rebalancing |
| Read path | May check several levels | Follows one tree path |
| Delete handling | Tombstones until safe to drop | Remove/update tree entries |
| Main challenge | Compaction policy and read amplification | Crash-safe page mutation |

This project chooses the LSM direction because immutable sorted files and
linear merges are simpler building blocks than a fully mutable, crash-safe
B+tree.

---

## 15. What Comes Next

Chapter 0700 defines the model but adds no code. The following chapters solve
the operational problems created by multiple files:

```text
0701  atomically store which SSTable file is current
0702  manage the database directory and metadata
0703  represent multiple SSTable levels
0704  merge levels according to size thresholds
later run compaction automatically and safely with concurrent writes
```

Chapter 0605 could rely on one fixed filename:

```text
main.sst
```

A multi-level engine has a changing collection of generated files:

```text
sst-001
sst-008
sst-019
...
```

The database must atomically record which files belong to the current state,
recover that list after a crash, and delete obsolete files only when durable
metadata stops referencing them.

---

## 16. Core Invariants

The later implementation must preserve these rules:

1. New writes enter the highest-priority level.
2. Upper-level versions override equal keys in lower levels.
3. Point reads search from newest to oldest.
4. Range reads merge all relevant sorted levels.
5. Tombstones remain until no older version can reappear.
6. Level capacities grow geometrically.
7. A new level file is published only after it is durable.
8. Old files are deleted only after durable metadata stops referencing them.

The concise mental model is:

```text
LSM-tree
=
several age-ordered data structures
+ newest-version-wins reads
+ geometric size limits
+ durable merge-and-publish transitions
```

Chapter 0700 turns Chapter 0605's single compaction operation into the general
architecture used by the rest of the storage engine.
