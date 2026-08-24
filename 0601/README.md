# Chapter 0601: Build SSTables

## Overview

Chapter 0600 explained how to publish a new disk state atomically. Chapter
0601 begins creating that disk state: it serializes the sorted in-memory
key/value array into an immutable sorted file.

That file is an **SSTable**:

```text
SSTable = Sorted String Table
```

Despite the historical name, its keys and values do not have to be strings.
They are arbitrary byte sequences. “Sorted” is the important part: records are
stored in key order so the file can later support binary search and ordered
iteration.

The chapter's pipeline is:

```text
append-only log ──replay/update──► sorted in-memory array
                                         │
                                         │ flush through iterator
                                         ▼
                              immutable sorted file
                                     (SSTable)
```

Chapter 0601 writes the file. Chapter 0602 will query it without loading the
whole file back into memory.

The central lesson is:

> Store a fixed-size offset index before variable-size sorted records. Knowing
> the record count lets both regions be written sequentially in one pass.

---

## 1. Why the Database Needs an SSTable

Before this chapter, the engine has:

```text
┌────────────────────────────────────────────────────┐
│ Append-only log on disk                            │
│                                                    │
│ SET a=1 → SET b=2 → SET a=3 → DEL b → ...         │
│                                                    │
│ Durable history, but not the primary query format  │
└────────────────────────┬───────────────────────────┘
                         │ replay
                         ▼
┌────────────────────────────────────────────────────┐
│ Sorted arrays in memory                            │
│                                                    │
│ keys: [a, c, d, ...]                               │
│ vals: [3, 7, 9, ...]                               │
│                                                    │
│ Queryable, but limited by available RAM             │
└────────────────────────────────────────────────────┘
```

The log makes updates durable, but the full current state still has to live in
memory. Persisting the compacted, sorted current state creates a disk structure
that later queries can access directly.

### Initial flush

The first SSTable is created by flushing the MemTable:

```text
MemTable
├── a → 3
├── c → 7
└── d → 9
        │
        │ iterate in sorted order
        ▼
SSTable file
├── a → 3
├── c → 7
└── d → 9
```

The log and MemTable mirror the same recent updates for different purposes:

| Structure | Primary purpose |
|---|---|
| Log | Fast durable append and crash recovery |
| MemTable | Queryable ordered view of recent updates in memory |
| SSTable | Immutable ordered state on disk |

### Later lifecycle

When the log eventually reaches a size limit, later chapters will combine the
old SSTable with newer in-memory state:

```text
old SSTable ───────┐
                   ├── merge in key order ──► new SSTable
new MemTable ──────┘                              │
                                                 │ atomic replacement
                                                 ▼
                                          active SSTable
```

The old file must not be overwritten while the new file is being constructed.
The CoW publication sequence from 0600 applies:

```text
build new file → flush/sync it → atomically publish it → reclaim old file
```

---

## 2. What an SSTable Is—and Is Not

An SSTable is an immutable file containing key/value records in sorted-key
order and supporting operations such as:

```text
seek to a key
read a record by position
iterate forward or backward in key order
```

It is not required to contain one particular internal data structure. An
SSTable might contain:

```text
sorted array
B+Tree-like blocks
prefix-compressed blocks
indexes plus data blocks
```

This chapter chooses the smallest useful representation: a fixed-size offset
array followed by variable-size KV records.

### Immutability

After an SSTable is built, existing records are not modified in place:

```text
update arrives
     ↓
record update elsewhere (log + MemTable)
     ↓
later build a replacement/merged SSTable
```

Immutability gives the file a simple and stable layout. It also connects the
chapter directly to the CoW and LSM ideas from 0600.

---

## Architectural Deep Dive: From Random Writes to an LSM

The byte format in 0601 is only one piece of a larger storage-engine design.
The complete problem is:

> Incoming writes arrive in arbitrary order, but an SSTable must be emitted in
> sorted-key order.

The solution is to sort recent state in memory, protect it with an append-only
log, flush it into immutable sorted files, and merge those files later.

### Chapter boundary

Keep the distinction between the current implementation and its architectural
destination clear:

```text
0601 implements now
└── one sorted iterator
    └── one immutable SSTable file

Architectural direction in later chapters
├── WAL rotation
├── mutable and frozen MemTables
├── multiple SSTable segments
├── newest-to-oldest reads
├── tombstone handling
├── background compaction
├── Bloom filters
└── LSM levels
```

The larger design explains why the interfaces and file format in this chapter
are useful; it does not mean all those later mechanisms already exist in 0601.

### The complete structure tree

```text
LSM-style storage engine
│
├── WAL / append-only log                           disk, chronological
│   └── enough information to recover recent writes
│
├── mutable MemTable                                memory, sorted
│   └── accepts new writes
│
├── frozen MemTable, when a flush is running        memory, sorted
│   └── stable input to the SSTable builder
│
└── immutable SSTables                              disk, sorted
    ├── newest segment
    ├── next-newest segment
    ├── older segment
    └── compacted lower levels, later
```

---

### 2A. Random writes become sorted MemTable state

Suppose writes arrive in this arbitrary order:

```text
1. SET mango  = 10
2. SET apple  = 4
3. SET zebra  = 7
4. SET banana = 2
5. SET apple  = 9
6. DEL mango
```

The log preserves that chronological order:

```text
WAL
├── SET mango=10
├── SET apple=4
├── SET zebra=7
├── SET banana=2
├── SET apple=9
└── DEL mango
```

The MemTable instead represents the latest logical state in key order:

```text
MemTable
├── apple  → 9
├── banana → 2
├── mango  → TOMBSTONE
└── zebra  → 7
```

Two transformations happened:

- chronological operations became key-sorted state;
- older versions inside the MemTable were replaced by newer ones.

`apple=4` is hidden by `apple=9`. `mango=10` is hidden by the newer deletion.

#### How an in-memory tree maintains order

A practical MemTable can use a Red-Black tree, AVL tree, skip list, or another
ordered in-memory structure. If keys arrive as:

```text
mango, apple, zebra, banana
```

a simplified search tree might look like:

```text
              mango
             /     \
        apple       zebra
            \
            banana
```

An in-order traversal emits:

```text
apple → banana → mango → zebra
```

Thus insertion order does not determine iteration order.

For a balanced tree:

| Operation | Typical cost |
|---|---:|
| Insert/update | `O(log N)` |
| Point lookup | `O(log N)` |
| Produce every key in sorted order | `O(N)` |

The teaching engine's sorted slices provide the same observable ordering, but
insertion may shift `O(N)` elements. The SSTable builder is intentionally
isolated behind `SortedKVIter`, so a future balanced tree or skip list can
replace the slices without changing the file writer.

---

### 2B. One write has two representations

For a durable update such as:

```text
SET apple = 9
```

the conceptual write path is:

```text
client
  │
  │ SET apple=9
  ▼
append checksummed operation to WAL
  │
  │ persistence barrier/group commit
  ▼
update sorted MemTable
  │
  ▼
acknowledge when recovery can reproduce the write
```

The WAL and MemTable contain related information but serve different jobs:

```text
WAL
├── ordered by time
├── cheap sequential append
├── durable after sync
└── used to rebuild recent state

MemTable
├── ordered by key
├── efficient lookup and range iteration
├── volatile RAM state
└── used to build the next SSTable
```

The WAL does not need sorted keys. Its job is to preserve operation order for
replay, not to answer normal queries.

---

### 2C. MemTable rotation and flushing

When the MemTable reaches a configured threshold—often based on bytes rather
than record count—it must be flushed.

A simplified description says “write the MemTable, then empty it.” A practical
engine normally rotates it so new writes can continue:

```text
Before rotation

incoming writes
      │
      ▼
Mutable MemTable A, full


After rotation

incoming writes ──► new Mutable MemTable B

                     old MemTable A
                          │ frozen; no more changes
                          ▼
                   background SSTable build
```

The frozen MemTable provides a stable sorted iterator:

```text
Frozen MemTable A
├── apple  → 9
├── banana → 2
├── mango  → TOMBSTONE
└── zebra  → 7
        │
        │ SortedKVIter
        ▼
SSTable S3
├── apple  → 9
├── banana → 2
├── mango  → TOMBSTONE
└── zebra  → 7
```

No second sorting step is needed. The MemTable already supplies keys in the
order required by the SSTable file.

#### Safe flush ordering

The old MemTable and its WAL range must not be discarded merely because file
construction started:

```text
1. Freeze MemTable A.
2. Start a new mutable MemTable B.
3. Build a candidate SSTable from A's sorted iterator.
4. Flush application buffers.
5. fsync the candidate SSTable.
6. Atomically publish it in the active database metadata.
7. Make the publication metadata durable.
8. Only now retire the corresponding WAL range.
9. Release frozen MemTable A when no reader needs it.
```

Discarding recoverable state too soon creates a loss window:

```text
discard WAL and MemTable
          ↓
crash before SSTable is durable/published
          ↓
no surviving representation of the updates
```

---

### 2D. Crash recovery rebuilds the MemTable

Suppose recent writes were acknowledged but not yet flushed:

```text
WAL on disk
├── SET apple=9
├── SET banana=2
└── DEL mango

MemTable in RAM
├── apple  → 9
├── banana → 2
└── mango  → TOMBSTONE
              ⚡ power loss destroys RAM
```

Recovery uses the durable representations:

```text
open published SSTables
          │
          ▼
read recent WAL records chronologically
          │
          ▼
apply them to a fresh ordered MemTable
          │
          ▼
recent sorted state restored
```

The rebuilt MemTable becomes:

```text
apple  → 9
banana → 2
mango  → TOMBSTONE
```

The WAL can be unsorted because replaying it into an ordered structure restores
sorted state.

---

### 2E. Multiple immutable segments and newest-first reads

Successive flushes create multiple SSTables:

```text
S1 — oldest
├── apple → 4
├── mango → 10
└── pear  → 6

S2
├── apple  → 7
├── banana → 2
└── carrot → 5

S3 — newest
├── apple → 9
├── mango → TOMBSTONE
└── zebra → 7
```

Because files are immutable, different versions of a key can coexist:

```text
apple in S1 → 4
apple in S2 → 7
apple in S3 → 9
```

The newest visible version wins.

The point-read search order is therefore:

```text
GET key
  │
  ├── 1. mutable MemTable
  ├── 2. frozen MemTable, if present
  ├── 3. newest SSTable
  ├── 4. next-newest SSTable
  └── 5. continue toward oldest relevant data
```

#### Mock read: a recent value

```text
GET apple
  │
  ├── mutable MemTable → absent
  ├── frozen MemTable  → absent
  └── S3               → apple=9

return 9; do not continue to older versions
```

#### Mock read: an old surviving value

```text
GET pear
  │
  ├── MemTables → absent
  ├── S3        → absent
  ├── S2        → absent
  └── S1        → pear=6

return 6
```

---

### 2F. Tombstones prevent deleted values from returning

SSTables cannot remove records in place. A deletion is recorded as a newer
special value:

```text
TOMBSTONE = “this key is deleted; hide older versions”
```

For `mango`:

```text
S3, newest → mango=TOMBSTONE
S1, older  → mango=10
```

The correct read is:

```text
GET mango
  │
  └── find TOMBSTONE in S3
          ↓
      return not found and stop
```

Continuing past the tombstone would resurrect deleted data:

```text
incorrectly ignore S3 tombstone
          ↓
find mango=10 in S1
          ↓
deleted value incorrectly reappears
```

Tombstones can be removed only when compaction knows that every older value
they hide is also being eliminated or can no longer be consulted.

---

### 2G. Why background compaction is required

Without compaction, every flush adds another file forever:

```text
S1 → S2 → S3 → S4 → S5 → S6 → ...
```

That causes:

- more files to check per read;
- multiple obsolete versions consuming disk space;
- tombstones accumulating;
- increasing file and metadata overhead.

Compaction reads several sorted inputs and produces fewer sorted outputs:

```text
old SSTable S1 ──┐
                 ├── sorted merge + version resolution ──► compacted SSTable
new SSTable S2 ──┘
```

#### Mock compaction

```text
S1, older
├── apple  → 4
├── banana → 2
├── mango  → 10
└── pear   → 6

S2, newer
├── apple  → 9
├── carrot → 5
└── mango  → TOMBSTONE
```

Merge resolution:

```text
apple:  4 versus 9         → keep newer 9
banana: only in S1         → keep 2
carrot: only in S2         → keep 5
mango: 10 versus tombstone → deletion wins
pear:   only in S1         → keep 6
```

Conceptual output:

```text
Compacted SSTable
├── apple  → 9
├── banana → 2
├── carrot → 5
├── mango  → TOMBSTONE, unless safe to discard
└── pear   → 6
```

Because both inputs are sorted, merging works like merge sort:

```text
cursor in S1 ──┐
               ├── compare current keys → output smaller/newer → advance
cursor in S2 ──┘
```

Every input record is processed approximately once, so merging files of sizes
`N` and `M` costs `O(N + M)` plus output I/O.

Compaction does not modify its inputs. It builds a new immutable output,
publishes it atomically, and then reclaims obsolete input files when safe—the
same CoW lifecycle studied in 0600.

---

### 2H. Missing keys and Bloom filters

An existing recent key may be found quickly. A missing key is harder:

```text
GET pineapple
  │
  ├── mutable MemTable → no
  ├── frozen MemTable  → no
  ├── newest SSTable   → no
  ├── next SSTable     → no
  ├── ...
  └── oldest SSTable   → no
```

The engine cannot conclude “not found” until every relevant structure has been
excluded. This is **read amplification**: one logical read checks multiple
physical structures.

A Bloom filter is a small probabilistic summary of an SSTable's keys:

```text
SSTable
├── Bloom filter
│   └── quick approximate membership test
└── sorted records
```

It answers:

| Answer | Meaning | Action |
|---|---|---|
| Definitely absent | The key is not in this SSTable | Skip the file |
| Maybe present | It could be present; false positive is possible | Check the file |

A correctly constructed Bloom filter may say “maybe” for a missing key, but it
must not say “definitely absent” for a key that was inserted into that filter.

Bloom filters are especially valuable for negative lookups because they can
avoid many unnecessary disk reads. They are a later optimization, not part of
the 0601 file format.

---

### 2I. Sorted structures make range queries natural

For a query such as:

```text
banana <= key <= mango
```

each structure seeks to the lower boundary:

```text
mutable MemTable iterator ──► first key >= banana
frozen MemTable iterator  ──► first key >= banana
SSTable S3 iterator       ──► first key >= banana
SSTable S2 iterator       ──► first key >= banana
SSTable S1 iterator       ──► first key >= banana
```

Their ordered streams are merged:

```text
MemTable ─────┐
frozen table ─┤
S3 ───────────┤
S2 ───────────┼──► k-way merge by key and recency ──► ordered results
S1 ───────────┘
```

For duplicate keys, the newest version wins. Tombstones suppress deleted keys.
Iteration stops after passing `mango`.

This is why sorted storage supports range scans naturally while a hash table,
which scatters keys by hash, does not.

---

### 2J. The three amplification costs

The LSM design trades cheap writes for background and read-side work:

| Cost | Meaning | Cause |
|---|---|---|
| Read amplification | One lookup checks multiple structures | Several MemTables/SSTables may contain the key |
| Write amplification | The same logical data is rewritten during compaction | Files are merged into new files |
| Space amplification | Multiple versions and tombstones coexist temporarily | Immutable files cannot be edited immediately |

Compaction policies, Bloom filters, indexes, caching, and level organization
balance these costs; they do not make every cost disappear.

---

### 2K. Update-in-place B-tree versus CoW B-tree versus LSM

A traditional B-tree normally identifies nodes by stable logical page numbers
or file offsets. Suppose a parent contains:

```text
Root page 10
└── child_page_id = 42
```

Page 42 contains:

```text
[alice=100, bob=200, carol=300]
```

To change `bob=200` to `bob=250`, an update-in-place B-tree overwrites the
contents at the same logical location:

```text
Before:
page 42 → [alice=100, bob=200, carol=300]

After:
page 42 → [alice=100, bob=250, carol=300]
```

The parent remains unchanged:

```text
Root page 10
└── child_page_id = 42   ← same page number
```

“The page location does not change” therefore means that the logical page ID
and file offset remain stable while the bytes stored there are replaced. The
SSD may internally remap physical flash cells, but the database still addresses
logical page 42.

The danger is a crash during the overwrite:

```text
page 42 → [part old bytes | part new bytes]
                                  ⚡
```

A traditional update-in-place B-tree consequently needs a recovery protocol,
commonly a write-ahead log:

```text
persist recovery information
        ↓
overwrite page 42
        ↓
use WAL to redo/undo after a crash if necessary
```

#### CoW B-tree

A copy-on-write B-tree does not overwrite page 42:

```text
old page 42 → [alice=100, bob=200, carol=300]
new page 73 → [alice=100, bob=250, carol=300]
```

Because the updated leaf now has a different page number, its parent path is
copied up to a new root. After every new page is durable, the engine atomically
switches the root pointer:

```text
old root → old path → page 42
new root → new path → page 73
                    │
                    └── unchanged subtrees are shared
```

#### LSM

An LSM does not overwrite the old SSTable page and does not immediately copy a
tree path. It records the update in newer state:

```text
older SSTable S1 → bob=200
new MemTable/S2  → bob=250
```

Reads search newest state first, so `bob=250` wins. Background compaction later
creates replacement SSTables and deletes obsolete input files when safe.

#### Direct comparison

| Design | What happens to old data? | Where is the update written? | What selects the current value? |
|---|---|---|---|
| Update-in-place B-tree | Existing page bytes are overwritten | Same page ID/file offset | Existing parent pointer still points to that page |
| CoW B-tree | Old page and path remain temporarily | New page plus copied ancestor path | Atomic root-pointer switch |
| LSM | Old SSTable remains immutable temporarily | MemTable and later a newer SSTable | Newest visible version; compaction reconciles later |

The shortest explanation is:

```text
Update-in-place B-tree:
    same page address, different contents

CoW B-tree:
    new page address, copy its path, then switch the root

LSM:
    new immutable file/segment, choose newest version, compact later
```

---

### 2L. End-to-end mental model

```text
random incoming operations
          │
          ├── append chronologically ───────────────► WAL
          │                                           │
          │                                           │ crash recovery
          ▼                                           │
insert/update ordered in memory ◄─────────────────────┘
          │
          ▼
mutable MemTable
          │ reaches threshold
          ▼
frozen MemTable ──sorted iterator──► SSTable builder from 0601
                                          │
                                          ▼
                                newest immutable segment
                                          │
                          ┌───────────────┴────────────────┐
                          │                                │
                          ▼                                ▼
                   newest-first reads             background compaction
                          │                                │
                          ▼                                ▼
                 current visible value        fewer/larger sorted segments
```

An office analogy can make the responsibilities memorable:

```text
WAL        = chronological receipt book
MemTable   = sorted working desk
SSTable    = sealed sorted filing cabinet
Compaction = combine cabinets and remove obsolete papers
Bloom      = label saying a cabinet definitely lacks a requested key
```

The basic loop is:

```text
receive randomly
    ↓
record durably
    ↓
organize in memory
    ↓
seal sorted files
    ↓
search newest first
    ↓
merge in the background
```

This architectural context explains why 0601 accepts a sorted iterator and
produces an immutable file: that small interface is the bridge between the
MemTable write path and the future LSM read/compaction paths.

---

## 3. File Layout: A Tree View

The complete file has three logical parts:

```text
SSTable file
├── Key count
│   └── n                                           8 bytes
│
├── Offset index                                   n × 8 bytes
│   ├── offset[0] ──► beginning of KV0
│   ├── offset[1] ──► beginning of KV1
│   ├── ...
│   └── offset[n-1] ──► beginning of KV(n-1)
│
└── Record region                                  variable size
    ├── KV0
    │   ├── key length                              4 bytes
    │   ├── value length                            4 bytes
    │   ├── key bytes                               variable
    │   └── value bytes                             variable
    ├── KV1
    ├── ...
    └── KV(n-1)
```

The compact linear picture from the book is:

```text
[ n keys | offset0 | offset1 | ... | offset(n-1) | KV0 | KV1 | ... | KV(n-1) ]
   8 B       8 B       8 B             8 B
```

Each KV record is:

```text
[ key length | value length | key bytes | value bytes ]
      4 B           4 B        keyLen       valueLen
```

All integers in the chapter implementation are encoded little-endian.

### Why two regions?

Records are variable-sized, so record `i` cannot be found with a formula such
as `base + i*recordSize`. The fixed-size offset index solves that:

```text
logical record position i
          ↓
read offset[i]
          ↓
absolute file position of KVi
```

This gives the file array-like navigation even though its records have
different lengths.

---

## 4. Exact Mock File: `x → 1`, `y → 234`

Use two sorted pairs:

```text
0: key="x", value="1"
1: key="y", value="234"
```

The number of keys is:

```text
n = 2
```

Therefore the first record begins after:

```text
8-byte count + 2 × 8-byte offsets
= 8 + 16
= byte 24
```

### Record sizes

`KV0` stores `x → 1`:

```text
4 bytes key length
+ 4 bytes value length
+ 1 byte  key "x"
+ 1 byte  value "1"
= 10 bytes
```

Therefore:

```text
offset[0] = 24
offset[1] = 24 + 10 = 34
```

`KV1` stores `y → 234`:

```text
4 bytes key length
+ 4 bytes value length
+ 1 byte  key "y"
+ 3 bytes value "234"
= 12 bytes
```

Final file size:

```text
34 + 12 = 46 bytes
```

### Tree visualization of the file

```text
SSTable: 46 bytes
│
├── bytes 0..7: nkeys = 2
│
├── offset index
│   ├── bytes 8..15:  offset[0] = 24 ───────────────┐
│   └── bytes 16..23: offset[1] = 34 ──────────┐   │
│                                               │   │
└── record region                               │   │
    │                                           │   │
    ├── byte 24: KV0 ◄──────────────────────────┼───┘
    │   ├── bytes 24..27: keyLen = 1            │
    │   ├── bytes 28..31: valLen = 1            │
    │   ├── byte 32:       key = "x"            │
    │   └── byte 33:       value = "1"          │
    │                                           │
    └── byte 34: KV1 ◄──────────────────────────┘
        ├── bytes 34..37: keyLen = 1
        ├── bytes 38..41: valLen = 3
        ├── byte 42:       key = "y"
        └── bytes 43..45:  value = "234"
```

### Linear byte map

```text
byte range   field             decoded value
──────────   ───────────────   ─────────────
0..7         number of keys    2
8..15        offset[0]         24
16..23       offset[1]         34
24..27       KV0 key length    1
28..31       KV0 value length  1
32           KV0 key           x
33           KV0 value         1
34..37       KV1 key length    1
38..41       KV1 value length  3
42           KV1 key           y
43..45       KV1 value         234
```

The actual byte sequence is:

```text
02 00 00 00 00 00 00 00    // nkeys = 2
18 00 00 00 00 00 00 00    // offset 24
22 00 00 00 00 00 00 00    // offset 34
01 00 00 00                // keyLen("x") = 1
01 00 00 00                // valLen("1") = 1
78                         // ASCII "x"
31                         // ASCII "1"
01 00 00 00                // keyLen("y") = 1
03 00 00 00                // valLen("234") = 3
79                         // ASCII "y"
32 33 34                   // ASCII "234"
```

In little-endian notation, decimal `24` is hexadecimal `0x18`, whose lowest
byte comes first. Decimal `34` is `0x22`.

---

## 5. Offset Arithmetic

For `n` records, the first record begins at:

```text
dataStart = 8 + 8*n
```

For the first record:

```text
offset[0] = dataStart
```

Each record occupies:

```text
recordSize(i) = 4 + 4 + len(key[i]) + len(value[i])
              = 8 + len(key[i]) + len(value[i])
```

The following offset is:

```text
offset[i+1] = offset[i] + recordSize(i)
```

More generally:

```text
offset[i] = dataStart
          + sum(recordSize(j)) for every j before i
```

### Why store both lengths?

The distance between adjacent offsets reveals a record's total size:

```text
offset[i+1] - offset[i]
    = 8 + keyLen + valueLen
```

That makes part of the format redundant. If the key length is known, value
length can be derived from total record size. The final record would use the
file size as its ending boundary.

The chapter stores both lengths anyway because explicit metadata makes each KV
record independently straightforward to decode:

```text
read keyLen
read valueLen
read exactly keyLen key bytes
read exactly valueLen value bytes
```

Simple formats often accept a few redundant bytes to reduce parsing complexity.

### Three-record offset map

For a second example, use three records with progressively larger keys and
values:

```text
KV0: a   → 1
KV1: bb  → 22
KV2: ccc → 333
```

The metadata occupies:

```text
8-byte key count + 3 × 8-byte offsets
= 8 + 24
= 32 bytes
```

The record sizes are:

```text
KV0 size = 8-byte length header + 1-byte key + 1-byte value = 10
KV1 size = 8-byte length header + 2-byte key + 2-byte value = 12
KV2 size = 8-byte length header + 3-byte key + 3-byte value = 14
```

Therefore:

```text
bytes 0..7    nkeys = 3

bytes 8..15   offset[0] = 32
bytes 16..23  offset[1] = 42
bytes 24..31  offset[2] = 54

bytes 32..41  KV0: a → 1
bytes 42..53  KV1: bb → 22
bytes 54..67  KV2: ccc → 333
```

Complete tree:

```text
SSTable, 68 bytes
│
├── bytes 0..7: nkeys=3
│
├── offset table
│   ├── bytes 8..15:  offset[0]=32 ───────────────┐
│   ├── bytes 16..23: offset[1]=42 ───────────┐   │
│   └── bytes 24..31: offset[2]=54 ───────┐   │   │
│                                          │   │   │
└── records                                │   │   │
    ├── byte 32: KV0 ◄─────────────────────┼───┼───┘
    │   ├── bytes 32..35: keyLen=1         │   │
    │   ├── bytes 36..39: valLen=1         │   │
    │   ├── byte 40:       key="a"         │   │
    │   └── byte 41:       val="1"         │   │
    │                                      │   │
    ├── byte 42: KV1 ◄─────────────────────┼───┘
    │   ├── bytes 42..45: keyLen=2         │
    │   ├── bytes 46..49: valLen=2         │
    │   ├── bytes 50..51: key="bb"         │
    │   └── bytes 52..53: val="22"         │
    │                                      │
    └── byte 54: KV2 ◄─────────────────────┘
        ├── bytes 54..57: keyLen=3
        ├── bytes 58..61: valLen=3
        ├── bytes 62..64: key="ccc"
        └── bytes 65..67: val="333"
```

The two cursor formulas play different roles:

```text
dataOffset = 8 + 8*nkeys
    reserves the complete offset table and starts KV0 at byte 32

indexOffset = 8 + 8*writtenKeys
    selects byte 8, 16, or 24 for the current offset entry
```

---

## 6. Why Sorted Order Matters

The offset array tells the engine where record `i` starts. Sorted keys tell it
which positions to investigate.

Suppose the file contains:

```text
position 0 → apple
position 1 → banana
position 2 → grape
position 3 → orange
position 4 → pear
```

A later lookup for `grape` can binary-search logical positions:

```text
search positions 0..4
        │
        ├── inspect position 2
        │       ↓
        │   offset[2]
        │       ↓
        │   decode key "grape"
        │
        └── match found
```

Logical binary search takes `O(log N)` key comparisons. Iteration can walk
positions `0, 1, 2, ...` in order.

Chapter 0601 prepares this structure but does not implement its query path.
Reading the `n`th record and performing binary search arrive in 0602.

If input keys are not sorted, the file is still writable, but its name and
future binary-search behavior become false. Sorted input is therefore a
precondition of `CreateFromSorted`, not an optional optimization.

---

## 7. The Input Abstraction

The builder does not depend directly on `KV`, `[][]byte`, or one MemTable
implementation. It accepts this conceptual interface:

```go
type SortedKV interface {
    Size() int
    Iter() (SortedKVIter, error)
}

type SortedKVIter interface {
    Valid() bool
    Key() []byte
    Val() []byte
    Next() error
    Prev() error
}
```

### Interface tree

```text
CreateFromSorted(input SortedKV)
│
├── input.Size()
│   └── tells builder how large the offset index is
│
└── input.Iter()
    └── SortedKVIter
        ├── Valid()  → is there a current record?
        ├── Key()    → current sorted key
        ├── Val()    → current value
        ├── Next()   → move forward
        └── Prev()   → move backward
```

The existing `KVIterator` already has the iterator shape. Go interfaces are
satisfied implicitly, so the builder can consume any implementation with the
required method set.

Possible producers include:

```text
SortedKV
├── current in-memory sorted arrays
├── a tiny test-only SortedArray
├── a future merged iterator over MemTable + SSTable
└── a future iterator merging multiple LSM levels
```

This boundary is important: the SSTable builder cares only that input is
sorted. It does not care how that order was produced.

### Why `Size()` is required

The builder must know where record data begins before it writes the first KV:

```text
record region begins at 8 + 8*Size()
```

Without the count, it would not know how much space to reserve for offsets.
It might need an extra pass, temporary storage, or a different format with a
footer. Supplying `Size()` enables a single-pass build.

### Count invariant

The iterator must yield exactly `Size()` records:

```text
declared count = records actually produced
```

Too few records leave unused or invalid index entries. Too many records make
the precomputed record start wrong. The reference implementation counts
emitted records and checks this invariant at the end.

---

## 8. Constructing the SSTable in One Pass

The builder maintains two logical cursors:

```text
index cursor = 8
data cursor  = 8 + 8*n
```

They advance through separate regions of the same file:

```text
file
│
├── count at byte 0
│
├── offset region
│   └── index cursor: 8 → 16 → 24 → ...
│
└── KV region
    └── data cursor: dataStart → next record → ...
```

### Construction algorithm

```text
1. Read n = input.Size().
2. Write n at byte 0.
3. Set indexPos = 8.
4. Set dataPos = 8 + 8*n.
5. Obtain the sorted iterator.
6. For each key/value pair:
   a. Write dataPos into the next offset slot.
   b. Write key length at dataPos.
   c. Write value length after it.
   d. Write key bytes.
   e. Write value bytes.
   f. Advance indexPos and dataPos.
7. Verify that exactly n records were emitted.
8. Flush application buffers, if used.
9. fsync the file.
```

### Cursor trace for the mock example

```text
n = 2

initial:
    indexPos = 8
    dataPos  = 24

write x → 1:
    write offset 24 at indexPos 8
    write 10-byte KV at dataPos 24
    indexPos = 16
    dataPos  = 34

write y → 234:
    write offset 34 at indexPos 16
    write 12-byte KV at dataPos 34
    indexPos = 24
    dataPos  = 46

final:
    emitted records = 2
    final file size  = 46
```

Even though writes alternate logically between the offset and record regions,
each region itself advances sequentially.

---

## 9. `Write`, `Seek`, and `WriteAt`

An open file normally has one shared current position:

```text
Write(data)
    ↓
writes at current position
    ↓
automatically advances current position
```

`Seek()` changes that position. Alternating between two file regions with
`Seek()` would require continual coordination:

```text
seek to offset slot → write offset
seek to data region → write KV
seek back to next offset slot → write offset
...
```

`WriteAt()` instead names the destination explicitly:

```go
fp.WriteAt(data, absoluteOffset)
```

It does not depend on the file's current position. The builder can therefore
maintain independent index and data offsets:

```text
WriteAt(encoded record offset, indexPos)
WriteAt(encoded KV header,     dataPos)
WriteAt(key bytes,             dataPos + 8)
WriteAt(value bytes,           dataPos + 8 + keyLen)
```

Explicit positions also make the file-format arithmetic visible in the code.

---

## 10. Two Sequential Streams in One File

The one-pass build may look random because it writes two file regions in
alternation. Structurally, however, it contains two forward-only streams:

```text
                        one SSTable file
                               │
             ┌─────────────────┴──────────────────┐
             │                                    │
             ▼                                    ▼
offset stream                                 record stream
starts at byte 8                              starts at 8 + 8*n
             │                                    │
             ▼                                    ▼
off0 → off1 → off2                          KV0 → KV1 → KV2
```

This observation enables application-level buffering without opening the file
twice.

---

## 11. I/O Buffering: Two Different Layers

Linux normally sends file writes to the OS page cache first:

```text
application
    │ Write/WriteAt syscall
    ▼
OS page cache in RAM
    │ later writeback or fsync
    ▼
persistent storage
```

Repeated writes to the same dirty page may be combined by the OS before any
physical disk write. That helps the many small SSTable writes.

But a syscall still has overhead. The chapter's loop performs several small
writes per KV:

```text
8-byte offset
8-byte KV length header
key bytes
value bytes
```

Many of those are too small to justify individual system calls. Application
buffering groups them:

```text
many small application writes
          ↓
bufio.Writer memory buffer
          ↓ one larger write
OS page cache
          ↓ fsync
disk
```

### `Flush()` is not `Sync()`

These operations cross different boundaries:

```text
bufio.Flush()
    application buffer ──► OS page cache

file.Sync()
    OS page cache ────────► persistent storage
```

The correct order is:

```go
w.Flush()
fp.Sync()
```

Calling `Sync()` while data is still inside `bufio.Writer` does not persist
those buffered bytes, because they have not reached the file yet.

---

## 12. Adapting `WriterAt` to `Writer`

`bufio.NewWriter` expects an `io.Writer`:

```go
type Writer interface {
    Write(p []byte) (n int, err error)
}
```

An `os.File` supports `WriteAt`, but `bufio.Writer` does not manage an explicit
offset for that method. The chapter introduces a small adapter:

```go
type OffsetWriter struct {
    writer io.WriterAt
    offset int64
}

func (w *OffsetWriter) Write(data []byte) (n int, err error) {
    n, err = w.writer.WriteAt(data, w.offset)
    w.offset += int64(n)
    return n, err
}
```

`OffsetWriter` converts this:

```text
WriteAt(data, explicitly managed offset)
```

into the sequential behavior expected by `bufio.Writer`:

```text
Write(data) → write at private cursor → advance private cursor
```

### Two adapters, two buffers, one file

```text
                         *os.File
                         /      \
                        /        \
        OffsetWriter(indexPos)  OffsetWriter(dataPos)
                  │                    │
          bufio.Writer           bufio.Writer
                  │                    │
        offsets: 24, 34, ...     records: KV0, KV1, ...
```

Both adapters wrap the same `os.File`, but each owns a different cursor:

```text
index writer cursor = 8
data writer cursor  = 8 + 8*n
```

Both streams must be flushed before the file is synced:

```text
flush offset buffer
flush record buffer
fsync file
```

This is an optimization of syscall count. The on-disk format does not change.
A simpler implementation can use direct `WriteAt` calls first and add the two
buffers later.

---

## 13. Durability and Atomic Publication

Finishing `CreateFromSorted` requires the generated bytes to be durable:

```text
write count, offsets, and records
              ↓
flush any application buffers
              ↓
fsync the SSTable
```

But `fsync` alone does not make replacement of an already-active SSTable
atomic. The complete publication sequence is inherited from 0600:

```text
active.sst ──► old complete SSTable

1. Build candidate.tmp without modifying active.sst.
2. Flush candidate.tmp's application buffers.
3. fsync candidate.tmp.
4. Atomically rename candidate.tmp over active.sst.
5. fsync the parent directory.
6. Reclaim obsolete storage later.
```

### Crash picture

```text
Crash during candidate construction:
    active.sst → old complete file
    candidate  → incomplete and ignored

Crash after candidate sync but before publication:
    active.sst → old complete file
    candidate  → complete but not current

Crash after durable publication:
    active.sst → new complete file
```

The initial SSTable may be reconstructible from the durable log. Nevertheless,
the engine should not advertise or depend on a partially constructed file.

This chapter focuses on constructing and syncing the file. Coordinating log
reset, MemTable reset, active-file metadata, and recovery is developed in later
steps.

---

## 14. Why One SSTable Is Still `O(N)`

The log prevents a whole-file rewrite for every individual update:

```text
small updates → append cheaply to log
many updates  → accumulate in MemTable
batch         → flush/merge into SSTable
```

That amortizes work across a batch. But if there is only one on-disk sorted
file, every flush must eventually merge with and replace that complete file:

```text
old file with N records
        +
new batch
        ↓
scan and rewrite approximately N records
        ↓
new file
```

The merge is still `O(N)` in the old file size. A sequence of growing full-file
rewrites remains too expensive at scale.

Later LSM chapters solve this by keeping multiple immutable SSTables organized
by size or level instead of immediately merging every update into one file.
The file format built here remains reusable as one component of that larger
design.

---

## 15. Space and Time Costs

For `n` records with total key and value payload `P` bytes:

```text
count header:             8 bytes
offset index:           8*n bytes
per-record length data: 8*n bytes
key/value payload:        P bytes
────────────────────────────────
total:              8 + 16*n + P bytes
```

Construction costs:

| Operation | Cost |
|---|---|
| Iterate through sorted input | `O(n)` |
| Encode and write payload | `O(P)` bytes |
| Build offset index | `O(n)` |
| Extra whole-input materialization | Not required |
| Final durability barrier | One file sync after all buffered data is flushed |

The builder is streaming in the sense that it consumes records one at a time.
It needs the record count up front, but it does not need to collect a second
complete copy of all records in application memory.

---

## 16. Common Misunderstandings

### “SSTable means all keys and values are text strings.”

No. The format stores `[]byte`. The name is historical.

### “An SSTable is necessarily a tree.”

No. It is an immutable sorted on-disk table. This chapter implements it as an
indexed array of variable-size KV records.

### “The offsets are relative to the record region.”

In this format they are absolute byte positions from the beginning of the
file. For the two-record example, the first offset is `24`, not `0`.

### “The offset itself points directly to key bytes.”

It points to the beginning of the KV record, where the key-length and
value-length fields appear before the key.

### “`bufio.Flush()` makes the SSTable durable.”

No. It moves bytes from the application buffer to the OS. `fsync`/`File.Sync`
is still needed for durability.

### “Calling `File.Sync()` before flushing `bufio.Writer` is enough.”

No. Bytes still held in the application buffer are not yet part of the file.
Flush every application buffer first, then sync.

### “The iterator can produce any order because the builder records offsets.”

No. Offsets provide locations, not sorting. The input must already be sorted
for future binary search and ordered iteration to work.

### “The iterator count is only a hint.”

No. `Size()` determines the exact index-region size and the first record
offset. It must match the number of records emitted.

### “The SSTable must be reopened and fully decoded into arrays.”

No. The offset index allows later chapters to address records directly in the
file. Full deserialization is unnecessary.

### “0601 already implements lookup from the SSTable.”

No. This chapter defines and writes the format. Chapter 0602 adds indexed
reading, iteration, and search.

---

## 17. Implementation Map

```text
SortedFile.CreateFromSorted(input)
│
├── create/open output file
│
├── input.Size()
│   ├── write 8-byte nkeys
│   └── compute dataStart = 8 + 8*nkeys
│
├── input.Iter()
│   └── for each sorted KV
│       ├── write current data offset into index region
│       ├── write 4-byte key length
│       ├── write 4-byte value length
│       ├── write key bytes
│       ├── write value bytes
│       └── advance both cursors
│
├── verify emitted count == declared count
│
├── flush offset and data buffers, if present
│
└── File.Sync()
    └── SSTable contents become durable
```

The conceptual ownership is:

```text
SortedKV / iterator  owns how records are produced in order
SortedFile builder   owns file layout and byte encoding
OffsetWriter         owns one explicit sequential file cursor
bufio.Writer         owns syscall batching
os.File.Sync         owns the final durability barrier
```

---

## 18. Review Questions

### 1. Why persist the MemTable if the log already exists on disk?

The log is an append history optimized for durable updates. The SSTable is a
sorted query structure that can be accessed without replaying the complete log
or keeping the complete database state in RAM.

### 2. What are the SSTable's three logical regions?

An 8-byte key count, an array of 8-byte absolute record offsets, and a
variable-size region of encoded KV records.

### 3. Why is the offset array fixed-sized?

Each entry is an 8-byte file position, so offset `i` can be located directly at
`8 + 8*i`. That supports record-by-position access and later binary search.

### 4. Why does every record store lengths?

Keys and values have variable sizes. Their lengths tell the decoder exactly
where the key ends, where the value begins, and how many bytes to read.

### 5. Why must the builder know `Size()` before iteration?

It must reserve `8*n` bytes for the offset array and calculate the first record
position, `8 + 8*n`, before writing any KV data.

### 6. Why can the file be constructed in one pass?

The known count establishes both region boundaries. Two independent cursors
can then advance through the offset and KV regions while the input iterator is
consumed once.

### 7. What is the difference between `Write()` and `WriteAt()`?

`Write()` uses and advances one file-wide current position. `WriteAt()` writes
to an explicitly supplied absolute offset, which makes independent region
cursors convenient.

### 8. Why use two `bufio.Writer`s?

The offset array and KV area are two separate but internally sequential
streams. Each can batch small writes independently even though both eventually
target the same file.

### 9. What does `OffsetWriter` do?

It wraps `io.WriterAt`, remembers one offset, exposes ordinary `Write`, and
advances that private offset after each write. This makes an explicit-position
stream compatible with `bufio.Writer`.

### 10. Why must `Flush()` happen before `Sync()`?

`Flush()` transfers application-buffered bytes into the file/page cache.
`Sync()` can persist only bytes that have already reached the file.

### 11. For `x→1` and `y→234`, why are the offsets 24 and 34?

The count and two offsets occupy `8 + 2*8 = 24` bytes. The first record is ten
bytes, so the second begins at `24 + 10 = 34`.

### 12. Why is a single SSTable not the final performance solution?

Every merge into one growing file rewrites approximately `O(N)` existing data.
An LSM uses multiple SSTables and staged merges to avoid a full rewrite for
every flushed batch.

### 13. How does 0600's atomic-update lesson apply here?

Build and sync a new immutable file without touching the active one, then
atomically publish the new file name or metadata pointer. Only afterward may
the old file be reclaimed.

### 14. How can writes arrive randomly while an SSTable is sorted?

The WAL records writes in chronological arrival order, while an ordered
MemTable applies them into key order. Flushing uses the MemTable's sorted
iterator, so the SSTable receives sorted records without sorting during file
construction.

### 15. Why can the WAL remain unsorted?

Its purpose is recovery, not normal lookup. Replaying its chronological
operations into a fresh ordered MemTable reconstructs the latest sorted state.

### 16. Why freeze and rotate a full MemTable instead of immediately emptying it?

The frozen MemTable gives the background flush a stable input. A new mutable
MemTable can accept incoming writes concurrently, and the frozen one remains
available until its SSTable is durably published.

### 17. Why are multiple SSTables searched newest first?

Immutable files can contain different versions of the same key. The newest
visible version determines current state, so finding it allows the search to
stop before an older value is incorrectly returned.

### 18. Why is a tombstone treated like a value during reads and compaction?

It is the newest statement that a key is deleted. It must hide older values
until compaction has removed every older version that could otherwise
resurface.

### 19. What does background compaction accomplish?

It merge-sorts immutable files, retains the newest relevant versions, safely
processes deletions, reduces file count, and reclaims obsolete storage by
publishing replacement files.

### 20. What can a Bloom filter safely tell the read path?

It can say that a key is definitely absent from one SSTable, allowing that file
to be skipped, or that the key may be present, requiring a real lookup. A
“maybe” can be a false positive; “definitely absent” must be trustworthy.

### 21. What does it mean that a B-tree overwrite keeps the page location unchanged?

The engine replaces bytes at the same logical page ID or file offset, so parent
nodes can keep their existing child pointer. By contrast, a CoW B-tree writes a
new page and copies the ancestor path, while an LSM writes a newer immutable
segment and reconciles versions during compaction.

---

## Crucial Takeaways

- An SSTable is an immutable, sorted, on-disk key/value structure—not
  necessarily a tree and not limited to strings.
- The file begins with an 8-byte record count and `n` 8-byte absolute offsets,
  followed by variable-size KV records.
- Each KV record stores 4-byte key and value lengths followed by their raw
  bytes.
- The offset index gives array-like addressing over variable-length records.
- `Size()` fixes the boundary between index and record regions, enabling a
  single-pass build.
- A sorted iterator decouples file construction from the specific MemTable or
  merge implementation that produces records.
- Random writes are appended chronologically to the WAL but organized by key
  in the MemTable; flushing converts that ordered in-memory view into an
  SSTable.
- Safe rotation freezes a full MemTable, opens a new mutable one for incoming
  writes, and retains the old WAL range until the SSTable is durably published.
- Reads search current memory and immutable segments from newest to oldest;
  newer values and tombstones hide older versions.
- Background compaction merge-sorts SSTables, resolves versions, controls file
  count, and reclaims obsolete data without modifying input files in place.
- Bloom filters reduce read amplification for missing keys by proving that
  selected SSTables definitely do not contain the requested key.
- An update-in-place B-tree replaces bytes at the same logical page address; a
  CoW B-tree writes a new page and switches a copied root path; an LSM writes a
  newer immutable segment and compacts versions later.
- `WriteAt()` supports independent offset and data cursors in one file.
- Two `OffsetWriter`/`bufio.Writer` streams can batch small writes in the two
  sequential regions.
- `Flush()` moves application-buffered data to the OS; `Sync()` makes file
  data durable. They are not interchangeable.
- A candidate SSTable must be complete and durable before it is atomically
  published over the old file.
- One growing SSTable still has `O(N)` merge cost; later LSM levels reuse this
  file format while avoiding immediate full-file rewrites.
- Chapter 0601 builds the file. Chapter 0602 will navigate and query it.
