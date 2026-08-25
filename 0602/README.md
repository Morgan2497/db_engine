# Chapter 0602: Query SSTable

## Overview

Chapter 0601 built an SSTable by serializing sorted key/value records into an
immutable file. Chapter 0602 makes that file queryable without deserializing
the entire file back into in-memory arrays.

The transition is:

```text
0601: sorted records ──serialize──► SSTable file

0602: SSTable file ──ReadAt/index──► one requested record
                  └──binary search──► first key >= target
                  └──iterator───────► records in sorted order
```

The chapter's central idea is:

> The fixed-size offset table makes a variable-length SSTable behave like an
> on-disk array. Given array position `pos`, the database can directly locate
> and read that one key/value record.

The primary lookup path is:

```text
logical position
      │
      │ 8 + 8*pos
      ▼
offset slot in the file
      │
      │ decode uint64
      ▼
record's absolute file offset
      │
      │ read keyLen + valLen
      ▼
record payload
      │
      ├── key bytes
      └── value bytes
```

This chapter adds three capabilities:

1. `index(pos)` reads the record at a numbered position.
2. `SortedFileIter` traverses records forward and backward.
3. `Seek(target)` uses binary search to find the first key greater than or
   equal to `target`.

It also discusses `mmap` as an alternative way to access an SSTable through
the operating system's page cache. The chapter explains `mmap`; the 0602
implementation continues to use `ReadAt`.

---

## 1. Chapter Boundary

### What 0601 already provides

```text
SortedKV iterator
      │
      ▼
write nkeys
write one absolute offset per record
write variable-length records
sync the completed SSTable
```

The file format is already complete:

```text
[ nkeys | offset[0] | offset[1] | ... | offset[n-1] | KV0 | KV1 | ... ]
```

### What 0602 adds

```text
SortedFile
├── nkeys                    cached record count
├── index(pos)               record-by-position access
├── Size()                   number of records
├── Iter()                   begin ordered iteration
├── Seek(key)                lower-bound lookup
└── SortedFileIter
    ├── Valid()
    ├── Key()
    ├── Val()
    ├── Next()
    └── Prev()
```

### What 0602 does not add

This chapter does not yet add SSTables to the main `KV` read path or merge
the MemTable with an SSTable. It only gives one `SortedFile` the same sorted
query shape as the existing in-memory structure.

```text
Implemented now
└── query one SortedFile

Prepared for later
├── query MemTable and SSTable together
├── merge sorted structures
├── publish replacement SSTables
└── reclaim obsolete SSTables
```

---

## 2. Recap: The SSTable Is an On-Disk Array

Suppose the sorted input contains:

```text
position 0: x → 1
position 1: y → 234
```

The records have different sizes:

```text
KV0 = 8-byte length header + 1-byte key + 1-byte value
    = 10 bytes

KV1 = 8-byte length header + 1-byte key + 3-byte value
    = 12 bytes
```

Since `nkeys = 2`, the record region starts at:

```text
8-byte nkeys + 2 × 8-byte offsets
= 8 + 16
= byte 24
```

Therefore:

```text
KV0 starts at byte 24
KV1 starts at byte 24 + 10 = 34
```

The completed file is 46 bytes:

```text
SSTable
│
├── bytes 0..7
│   └── nkeys = 2
│
├── offset table
│   ├── bytes 8..15
│   │   └── offset[0] = 24 ───────────────────┐
│   │                                         │
│   └── bytes 16..23                          │
│       └── offset[1] = 34 ───────────────┐   │
│                                         │   │
└── record region                         │   │
    │                                     │   │
    ├── KV0 starts at byte 24 ◄───────────┼───┘
    │   ├── bytes 24..27: keyLen = 1      │
    │   ├── bytes 28..31: valLen = 1      │
    │   ├── byte 32:      key = "x"       │
    │   └── byte 33:      val = "1"       │
    │                                     │
    └── KV1 starts at byte 34 ◄───────────┘
        ├── bytes 34..37: keyLen = 1
        ├── bytes 38..41: valLen = 3
        ├── byte 42:      key = "y"
        └── bytes 43..45: val = "234"
```

Linear form:

```text
byte     0          8          16         24              34
         │          │          │          │               │
         ▼          ▼          ▼          ▼               ▼
       [ n=2  | offset0=24 | offset1=34 | KV0: x→1 | KV1: y→234 ]
```

Exact encoded bytes:

```text
bytes 0..7:    [2,  0, 0, 0, 0, 0, 0, 0]
bytes 8..15:   [24, 0, 0, 0, 0, 0, 0, 0]
bytes 16..23:  [34, 0, 0, 0, 0, 0, 0, 0]
bytes 24..31:  [1,  0, 0, 0, 1, 0, 0, 0]
bytes 32..33:  ['x', '1']
bytes 34..41:  [1,  0, 0, 0, 3, 0, 0, 0]
bytes 42..45:  ['y', '2', '3', '4']
```

### Why this acts like an array

The records themselves have variable sizes, so this does not work:

```text
recordAddress = dataStart + fixedRecordSize*pos
```

There is no fixed record size. However, every offset entry is exactly eight
bytes, so this does work:

```text
offsetSlotAddress = 8 + 8*pos
```

The fixed-size offset table gives direct access to the starting address of any
variable-size record:

```text
position 0 ──► file byte 8  ──contains──► 24 ──► KV0
position 1 ──► file byte 16 ──contains──► 34 ──► KV1
```

That indirection is the foundation of the whole chapter.

---

## 3. `nkeys`: Disk Metadata and Runtime State

0602 adds `nkeys` to `SortedFile`:

```go
type SortedFile struct {
	FileName string
	fp       *os.File
	nkeys    int
}
```

There are now two representations of the record count:

```text
SSTable bytes 0..7                 SortedFile object in RAM
┌───────────────────┐             ┌───────────────────┐
│ encoded nkeys = 2 │             │ nkeys: 2          │
└───────────────────┘             └───────────────────┘
 durable file metadata              convenient runtime value
```

The runtime value is used by:

```text
Size()              return record count
Valid()             check iterator bounds
index(pos)          validate pos and locate record region
findPos()           set binary-search range [0, nkeys)
```

When this chapter creates the file, `writeSortedFile` must preserve the count
on the object after successfully writing the records:

```text
number actually written ──► file.nkeys
```

Otherwise, the file could contain two records while the in-memory object
incorrectly reports zero.

The current chapter queries the `SortedFile` object that just created the
SSTable. A complete “open an existing SSTable after restarting the process”
path would need to open the file and decode `nkeys` from bytes `0..7`. That is
not the new operation implemented in this step.

---

## 4. `ReadAt`: The Reverse Direction of `WriteAt`

0601 used:

```go
file.fp.WriteAt(buf[:], offset)
```

Its byte direction is:

```text
RAM buf ─────────────► file bytes beginning at offset
source                  destination
```

0602 uses:

```go
file.fp.ReadAt(buf[:], offset)
```

Its byte direction is reversed:

```text
file bytes beginning at offset ─────────────► RAM buf
source                                         destination
```

The receiver is the same open file, but the method determines the direction:

| Operation | Source | Destination |
|---|---|---|
| `PutUint64(buf[:], value)` | encoded value | `buf` |
| `WriteAt(buf[:], offset)` | `buf` | file at `offset` |
| `ReadAt(buf[:], offset)` | file at `offset` | `buf` |
| `Uint64(buf[:])` | `buf` | returned Go number |

### What the two `ReadAt` arguments mean

```go
file.fp.ReadAt(buf[:], 16)
```

means:

```text
file.fp               the file to read from
buf[:]                where the bytes should be placed in RAM
16                    where reading begins inside the file
```

For an eight-byte buffer, the mapping is:

```text
file[16] ──► buf[0]
file[17] ──► buf[1]
file[18] ──► buf[2]
file[19] ──► buf[3]
file[20] ──► buf[4]
file[21] ──► buf[5]
file[22] ──► buf[6]
file[23] ──► buf[7]
```

`ReadAt` does not use or advance a hidden current file cursor. The code gives
it an absolute file position every time.

### Reading bytes is not decoding a number

After `ReadAt`, `buf` contains raw bytes:

```text
buf = [34, 0, 0, 0, 0, 0, 0, 0]
```

The code still needs:

```go
binary.LittleEndian.Uint64(buf[:])
```

to interpret those eight bytes as the Go number `34`.

The complete direction is:

```text
file bytes ──ReadAt──► buf ──Uint64/Uint32──► Go integer
```

---

## 5. `index(pos)`: Read One Record by Position

The most important new operation is conceptually:

```go
func (file *SortedFile) index(pos int) (key []byte, val []byte, err error)
```

Its contract is:

```text
input:  a logical sorted-array position
output: the key and value stored at that position
```

Examples:

```text
index(0) → "x", "1"
index(1) → "y", "234"
```

It does not scan from the beginning. It follows the offset indirection.

### Complete lookup tree

```text
index(pos)
│
├── verify 0 <= pos < nkeys
│
├── calculate offsetSlot = 8 + 8*pos
│
├── ReadAt 8 bytes from offsetSlot into buf
│
├── decode buf as uint64 recordOffset
│
├── verify recordOffset is not inside file metadata
│
├── ReadAt 8-byte record header from recordOffset
│
├── decode keyLen and valLen
│
├── allocate keyLen + valLen bytes
│
├── ReadAt payload from recordOffset + 8
│
└── split payload
    ├── data[:keyLen]  → key
    └── data[keyLen:]  → value
```

### Why three reads are needed

To read one variable-length record, the database needs three pieces of
information in order:

```text
Read 1: Where does the record begin?
        └── read its 8-byte offset slot

Read 2: How long are the key and value?
        └── read its 8-byte length header

Read 3: What are the key and value bytes?
        └── read exactly keyLen + valLen payload bytes
```

The reads cannot all be planned initially because read 2 depends on the value
decoded from read 1, and read 3 depends on the lengths decoded from read 2.

---

## 6. Exact Trace: `index(1)`

Use the same file:

```text
position 0: x → 1
position 1: y → 234
```

We call:

```text
index(1)
```

The desired result is:

```text
key = "y"
val = "234"
```

### Step 1: Validate the logical position

```text
nkeys = 2
pos   = 1

0 <= 1 < 2  → valid
```

Valid positions are zero-based:

```text
position 0: first record
position 1: second record
position 2: invalid because nkeys is 2
```

### Step 2: Calculate the offset-slot position

The formula is:

```text
offsetSlot = 8 + 8*pos
```

For `pos = 1`:

```text
offsetSlot = 8 + 8*1
           = 16
```

This number `16` is not yet the location of KV1. It is the location of the
offset entry that tells us where KV1 is.

```text
file byte 16
     │
     ▼
[ offset[1] = 34 ] ─────────► KV1 begins at file byte 34
```

Keep the two locations distinct:

```text
16 = location of the pointer-like offset entry
34 = location of the actual KV1 record
```

### Step 3: Read `offset[1]` into `buf`

Conceptual code:

```go
file.fp.ReadAt(buf[:], int64(8+8*pos))
```

With `pos = 1`:

```go
file.fp.ReadAt(buf[:], 16)
```

Before the call:

```text
FILE                                      RAM

bytes 16..23                              buf
[34,0,0,0,0,0,0,0]                       [?, ?, ?, ?, ?, ?, ?, ?]
```

During the call:

```text
FILE source                               RAM destination

file[16] = 34 ──────────────────────────► buf[0] = 34
file[17] = 0  ──────────────────────────► buf[1] = 0
file[18] = 0  ──────────────────────────► buf[2] = 0
file[19] = 0  ──────────────────────────► buf[3] = 0
file[20] = 0  ──────────────────────────► buf[4] = 0
file[21] = 0  ──────────────────────────► buf[5] = 0
file[22] = 0  ──────────────────────────► buf[6] = 0
file[23] = 0  ──────────────────────────► buf[7] = 0
```

After the call:

```text
buf = [34, 0, 0, 0, 0, 0, 0, 0]
```

### Step 4: Decode the record offset

```go
offset := int64(binary.LittleEndian.Uint64(buf[:]))
```

Direction:

```text
buf bytes [34,0,0,0,0,0,0,0]
                 │
                 │ decode little-endian uint64
                 ▼
            offset = 34
```

Now the code knows where KV1 begins.

### Step 5: Ensure the offset is not inside the metadata

The record region begins after the count and complete offset table:

```text
recordRegionStart = 8 + 8*nkeys
                  = 8 + 8*2
                  = 24
```

KV offsets must satisfy:

```text
offset >= 24
```

The decoded offset is `34`, so it passes:

```text
34 >= 24  → valid
```

If the offset were `16`, it would point back into the offset table:

```text
offset = 16
16 < 24
→ corrupted file
```

### Step 6: Reuse `buf` to read the record header

At this moment:

```text
offset = 34
```

The record header is eight bytes:

```text
4-byte keyLen | 4-byte valLen
```

The code reads:

```go
file.fp.ReadAt(buf[:], offset)
```

Equivalent here:

```go
file.fp.ReadAt(buf[:], 34)
```

Byte mapping:

```text
FILE bytes 34..41                       RAM buf[0..7]

[1,0,0,0,3,0,0,0] ─────ReadAt────────► [1,0,0,0,3,0,0,0]
 └ keyLen ─┘ └ valLen ─┘                  └ keyLen ─┘ └ valLen ─┘
```

The old encoded offset `34` is overwritten in `buf`, but the Go variable
`offset` already holds the decoded number `34`.

```text
Before header ReadAt:
buf    = encoded 34
offset = Go integer 34

After header ReadAt:
buf    = encoded keyLen 1 and valLen 3
offset = Go integer 34, unchanged
```

This is the read-side version of reusing the scratch buffer from 0601.

### Step 7: Decode the two lengths

```go
klen := binary.LittleEndian.Uint32(buf[0:4])
vlen := binary.LittleEndian.Uint32(buf[4:8])
```

The two four-byte views are:

```text
buf
┌───────────────┬───────────────┐
│ 1, 0, 0, 0    │ 3, 0, 0, 0    │
│ buf[0:4]      │ buf[4:8]      │
│ keyLen = 1    │ valLen = 3    │
└───────────────┴───────────────┘
```

After decoding:

```text
klen = 1
vlen = 3
```

### Step 8: Allocate exactly enough payload memory

```go
data := make([]byte, klen+vlen)
```

The payload size is:

```text
klen + vlen = 1 + 3 = 4 bytes
```

So RAM now has:

```text
data = [?, ?, ?, ?]
```

This allocation is only for the requested record. The whole SSTable is not
loaded into memory.

### Step 9: Read the payload

The record begins at byte `34`, but the payload begins after its eight-byte
length header:

```text
payloadOffset = offset + 8
              = 34 + 8
              = 42
```

The code reads:

```go
file.fp.ReadAt(data, offset+8)
```

Equivalent here:

```go
file.fp.ReadAt(data, 42)
```

Mapping:

```text
FILE                                      RAM

file[42] = 'y' ─────────────────────────► data[0] = 'y'
file[43] = '2' ─────────────────────────► data[1] = '2'
file[44] = '3' ─────────────────────────► data[2] = '3'
file[45] = '4' ─────────────────────────► data[3] = '4'
```

After the read:

```text
data = ['y', '2', '3', '4']
```

### Step 10: Split one allocation into key and value views

The key occupies the first `klen` bytes:

```go
key = data[:klen]
```

The value occupies everything after the key:

```go
val = data[klen:]
```

With `klen = 1`:

```text
one backing array

data
┌───────────┬───────────────────┐
│    'y'    │  '2'  '3'  '4'   │
└───────────┴───────────────────┘
  data[:1]       data[1:]
     │               │
     ▼               ▼
 key = "y"       val = "234"
```

The split does not copy the payload again. `key` and `val` are slice views
into the same `data` backing array.

The completed result is:

```text
index(1)
└── key = "y"
└── val = "234"
```

---

## 7. Position, Offset Slot, Record Offset, and Payload Offset

Four different numbers appear during `index(pos)`. They must not be confused:

```text
pos
│ logical array position: 1
│
├── offsetSlot = 8 + 8*pos
│   location of offset[1]: 16
│
├── recordOffset = value stored in offset[1]
│   location of KV1 header: 34
│
└── payloadOffset = recordOffset + 8
    location of KV1 key bytes: 42
```

For both records:

| `pos` | Offset-slot formula | Offset slot | Decoded record offset | Payload offset |
|---:|---:|---:|---:|---:|
| 0 | `8 + 8*0` | 8 | 24 | 32 |
| 1 | `8 + 8*1` | 16 | 34 | 42 |

Visual route for position 1:

```text
pos = 1
   │
   │ calculate 8 + 8*1
   ▼
file byte 16
   │
   │ read and decode offset[1]
   ▼
recordOffset = 34
   │
   ├── bytes 34..41: length header
   │
   └── payloadOffset = 34 + 8 = 42
       └── bytes 42..45: "y234"
```

---

## 8. Corruption and I/O Errors

Disk reads can fail. This is why the on-disk iterator returns errors from
`Next()` and `Prev()`, while a simple in-memory iterator often has no I/O
failure to report.

### Invalid caller position

The internal `index(pos)` expects:

```text
0 <= pos < file.nkeys
```

For two records:

```text
index(0)   valid
index(1)   valid
index(-1)  invalid
index(2)   invalid
```

The iterator checks `Valid()` before it tries to load the current position.

### Offset pointing into metadata

The file's metadata ends at:

```text
8 + 8*nkeys
```

An offset smaller than that points into `nkeys` or the offset table instead of
the record region:

```text
metadata                             record region
┌──────────────────────────────────┬─────────────────────
│ nkeys | offsets                  │ KV records
└──────────────────────────────────┴─────────────────────
0                                  8 + 8*nkeys
                                   ▲
                                   minimum valid KV offset
```

The chapter explicitly rejects that case as a corrupted file.

### Short or truncated file

`ReadAt` is asked to fill a specific destination slice. If the file ends
before all requested bytes can be read, it returns an error such as `io.EOF`.
That error propagates to the caller instead of silently manufacturing a
partial record.

Examples include:

```text
offset slot is truncated
record's 8-byte length header is truncated
payload is shorter than keyLen + valLen
```

### Scope of the corruption check

The chapter adds one useful structural check, but it is not a complete
production SSTable validator. A hardened format might also check:

```text
offsets are within file size
offsets are monotonically increasing
length addition cannot overflow
payload stays within file size
records remain in sorted-key order
checksum matches
```

Those are useful design considerations, not additional requirements to invent
while implementing the 0602 exercise.

---

## 9. Making `SortedFile` Implement `SortedKV`

0601 accepted a `SortedKV` as input:

```go
type SortedKV interface {
	Size() int
	Iter() (SortedKVIter, error)
}
```

0602 gives `SortedFile` those same operations:

```text
SortedFile
├── Size() int
└── Iter() (SortedKVIter, error)
```

This creates a powerful symmetry:

```text
in-memory sorted structure ─┐
                            ├──► SortedKV interface
on-disk SortedFile ─────────┘
```

An algorithm that only requires sorted iteration no longer needs to care
whether the records come from RAM or disk.

```text
consumer
   │
   │ asks only for Size and Iter
   ▼
SortedKV
├── in-memory implementation
└── file-backed implementation
```

The difference is that disk-backed iteration may return I/O errors.

---

## 10. `SortedFileIter`: State for One Current Record

The file iterator stores:

```go
type SortedFileIter struct {
	file *SortedFile
	pos  int
	key  []byte
	val  []byte
}
```

Ownership tree:

```text
SortedFileIter
├── file ──► source SSTable
├── pos     current logical record position
├── key     currently loaded key bytes
└── val     currently loaded value bytes
```

The iterator does not keep all file records in memory. It keeps only the
currently loaded record.

### `Valid()`

```text
Valid when 0 <= pos < file.nkeys
```

For `nkeys = 2`:

```text
pos = -1    invalid, before the first record
pos =  0    valid, KV0
pos =  1    valid, KV1
pos =  2    invalid, after the last record
```

Visualization:

```text
        before       valid positions          after
          │             │     │                 │
          ▼             ▼     ▼                 ▼
pos      -1             0     1                 2
                       KV0   KV1
```

### `loadCurrent()`

Moving `pos` is not enough. Since the data is on disk, the iterator must load
the corresponding record:

```text
change pos
    │
    ▼
is new pos valid?
    │
    ├── no  → iterator remains invalid
    │
    └── yes → file.index(pos)
                  │
                  ├── returned key → iter.key
                  └── returned val → iter.val
```

This helper centralizes the read and error propagation for `Iter`, `Seek`,
`Next`, and `Prev`.

### `Iter()`

Beginning an iteration means:

```text
create iterator at pos 0
        │
        ▼
load index(0)
        │
        ▼
return iterator positioned on first record
```

For the mock file:

```text
Iter()
└── pos = 0
    ├── key = "x"
    └── val = "1"
```

If the initial disk read fails, `Iter()` returns the error immediately.

### `Next()`

```text
current pos 0
      │
      │ increment
      ▼
new pos 1
      │
      │ load index(1)
      ▼
current record becomes y → 234
```

One more `Next()` gives:

```text
pos 1 ──increment──► pos 2 ──► invalid
```

### `Prev()`

```text
current pos 1
      │
      │ decrement
      ▼
new pos 0
      │
      │ load index(0)
      ▼
current record becomes x → 1
```

One more `Prev()` gives:

```text
pos 0 ──decrement──► pos -1 ──► invalid
```

### Do not use `Key()` or `Val()` when invalid

The iterator's meaningful contract is:

```go
for iter.Valid() {
	use(iter.Key(), iter.Val())
	if err := iter.Next(); err != nil {
		return err
	}
}
```

Once `Valid()` is false, callers must not treat `Key()` or `Val()` as a current
record. The iterator may still physically contain slices from the previously
loaded record, but those values are outside the valid iterator contract.

---

## 11. Iterator Execution Trace

For:

```text
KV0: x → 1
KV1: y → 234
```

the complete forward trace is:

```text
file.Iter()
│
├── construct {pos: 0}
├── loadCurrent()
│   └── index(0)
│       ├── read offset[0] = 24
│       ├── read keyLen=1, valLen=1
│       └── read "x1"
│
└── iterator state
    ├── Valid() = true
    ├── Key() = "x"
    └── Val() = "1"

iter.Next()
│
├── pos: 0 → 1
├── loadCurrent()
│   └── index(1)
│       ├── read offset[1] = 34
│       ├── read keyLen=1, valLen=3
│       └── read "y234"
│
└── iterator state
    ├── Valid() = true
    ├── Key() = "y"
    └── Val() = "234"

iter.Next()
│
├── pos: 1 → 2
├── loadCurrent() sees invalid pos
└── Valid() = false
```

The iteration performs record reads lazily as the iterator moves. It does not
first reconstruct this in RAM:

```text
keys = ["x", "y"]
vals = ["1", "234"]
```

---

## 12. `Seek`: Lower-Bound Binary Search

Because SSTable keys are sorted, lookup does not need to scan every record.
`Seek(target)` uses binary search over logical record positions.

Its result is the first record whose key is greater than or equal to the
target:

```text
result.key >= target
```

This is called a lower-bound search.

### Exact match

```text
keys:   [x, y]
target:  x

Seek("x") → x
```

### Missing key between records

```text
keys:   [x, y]
target:  xx

x < xx < y

Seek("xx") → y
```

This is the case tested by the chapter. `Seek` is not limited to exact-key
lookup; it positions an iterator where an ordered scan should begin.

### Target before the first key

```text
keys:   [x, y]
target:  a

Seek("a") → x
```

### Target after the last key

```text
keys:   [x, y]
target:  z

Seek("z") → pos 2 → invalid iterator
```

There is no key greater than or equal to `z`.

### Why lower-bound behavior is useful

It supports both point and range queries:

```text
point lookup for target
├── Seek(target)
├── iterator invalid?     → not found
├── iterator.Key==target? → found
└── otherwise             → not found; iterator is on next larger key
```

```text
range scan [start, end]
├── iter = Seek(start)
├── while iter.Valid()
├── stop if iter.Key() > end
├── consume current record
└── iter.Next()
```

---

## 13. How Binary Search Works

The search keeps a half-open candidate range:

```text
[lo, hi)
```

That means `lo` is included and `hi` is excluded.

Initially:

```text
lo = 0
hi = nkeys
```

For five keys:

```text
position:  0      1      2      3      4
key:      ant    cat    dog    fox    yak

initial candidate range: [0, 5)
```

Each iteration selects:

```go
mid := lo + (hi-lo)/2
```

Then it reads `index(mid)` and compares:

```go
r := bytes.Compare(target, keyAtMid)
```

`bytes.Compare` performs lexicographic byte comparison:

```text
r > 0   target is greater than keyAtMid
r < 0   target is less than keyAtMid
r = 0   keys are equal
```

The range update is:

```text
target > middle key  → discard middle and everything left
                       lo = mid + 1

target < middle key  → keep possible answer on the left
                       hi = mid

target == middle key → exact answer found
```

### Trace: seek `"eel"`

Use:

```text
position:  0      1      2      3      4
key:      ant    cat    dog    fox    yak
target:   eel
```

`eel` is absent. The desired lower bound is `fox` at position 3.

#### Iteration 1

```text
lo = 0
hi = 5
mid = 0 + (5-0)/2 = 2

index(2) → "dog"

"eel" > "dog"
→ lo = mid + 1 = 3
```

Remaining range:

```text
position:  0      1      2     [3      4]
key:      ant    cat    dog    fox    yak
```

#### Iteration 2

```text
lo = 3
hi = 5
mid = 3 + (5-3)/2 = 4

index(4) → "yak"

"eel" < "yak"
→ hi = mid = 4
```

Remaining range:

```text
position:  0      1      2     [3]     4
key:      ant    cat    dog    fox    yak
```

#### Iteration 3

```text
lo = 3
hi = 4
mid = 3 + (4-3)/2 = 3

index(3) → "fox"

"eel" < "fox"
→ hi = mid = 3
```

Now:

```text
lo = 3
hi = 3
```

The range is empty and binary search returns `lo`:

```text
result position = 3
result key      = "fox"
```

### Why `hi = mid`, not `mid - 1`

This algorithm searches the half-open range `[lo, hi)`. When the target is
less than the middle key, the middle key may itself be the first key greater
than the target, so it must remain a candidate:

```text
target < key[mid]
→ hi = mid
```

The new range excludes `hi`, but `mid` becomes the boundary and can be the
final returned position when `lo` reaches it.

### I/O-aware binary search

The binary search arithmetic is performed in RAM, but each comparison needs
the middle key from disk:

```text
choose mid in RAM
      │
      ▼
file.index(mid)
      │
      ├── read offset
      ├── read header
      └── read key/value payload
      │
      ▼
compare target and middle key
```

For `N` records, binary search makes `O(log N)` record lookups rather than an
`O(N)` linear scan. Those lookups may be random file accesses, so the operating
system's page cache and the file's block layout still matter in practice.

---

## 14. `Seek` Returns an Iterator

`findPos(target)` only finds a logical position. `Seek` turns that position
into a usable iterator:

```text
Seek(target)
│
├── findPos(target)
│   └── binary search returns pos
│
├── construct SortedFileIter{file, pos}
│
├── loadCurrent()
│   └── if valid, read index(pos)
│
└── return as SortedKVIter interface
```

For the chapter test:

```text
Seek("xx")
│
├── x < xx
├── xx < y
├── findPos returns 1
├── load index(1)
└── iterator
    ├── Valid() = true
    ├── Key() = "y"
    └── Val() = "234"
```

Returning an iterator is more useful than returning only a value:

```text
Seek("xx") → y
               │
               └── Next() can continue scanning later keys
```

The method returns the `SortedKVIter` interface instead of the concrete
`*SortedFileIter`. This keeps callers dependent on iterator behavior rather
than the file iterator's internal fields.

---

## 15. Error Flow

The on-disk query path is deliberately error-aware:

```text
ReadAt failure
    │
    ▼
index(pos) returns error
    │
    ├──► loadCurrent returns error
    │      ├──► Iter returns error
    │      ├──► Next returns error
    │      └──► Prev returns error
    │
    └──► findPos returns error
           └──► Seek returns error
```

This explains the interface:

```go
Next() error
Prev() error
```

Moving an in-memory array index is simple arithmetic. Moving a file-backed
iterator may require several file reads, any of which can fail.

The normal loop pattern is:

```go
iter, err := file.Iter()
for ; err == nil && iter.Valid(); err = iter.Next() {
	key := iter.Key()
	val := iter.Val()
	// use key and val
}
if err != nil {
	return err
}
```

The caller checks both termination conditions:

```text
Valid() == false  → normal end of iteration
err != nil        → abnormal I/O failure
```

---

## 16. Sequential Reads, Random Reads, and Caching

Iteration and binary search have different access patterns.

### Sequential iteration

```text
KV0 → KV1 → KV2 → KV3 → ...
```

The reads move forward through nearby file positions. `bufio.Reader` can help
sequential access by requesting a larger block and serving several small reads
from its application buffer.

### Binary search

```text
middle → quarter → three-quarters → nearby final position
```

The reads jump between positions. A simple sequential buffer may load many
bytes the search never uses.

```text
sequential scan
file: [A][B][C][D][E]
       └────────────►

binary search
file: [A][B][C][D][E]
               ▲
       ▲               ▲
             jumps
```

Caching random I/O inside the database is more complex because it requires a
page cache design:

```text
application page cache
├── divide file into pages
├── locate cached pages
├── load missing pages
├── track memory usage
├── select pages to evict
├── handle concurrency
└── coordinate dirty data, if writable
```

Production databases sometimes implement this cache themselves and use direct
I/O to avoid double caching. Others rely on the operating system's page cache.

---

## 17. `mmap`: Exposing File Pages as Memory

`mmap` maps a file region into a process's virtual address space and presents
it as bytes that can be accessed like memory:

```text
process virtual memory
┌───────────────────────────────────────┐
│ mapped []byte                         │
│   │                                   │
│   └──────── maps to ───────────────┐  │
└────────────────────────────────────┼──┘
                                     ▼
operating-system page cache
┌───────────────────────────────────────┐
│ cached pages from the SSTable         │
└───────────────────┬───────────────────┘
                    ▼
SSTable file on storage
```

Conceptual API:

```go
syscall.Mmap(fd, offset, length, prot, flags)
```

It returns a byte slice representing the mapped file region.

### A mapping does not necessarily read the entire file immediately

The operating system can load pages on demand:

```text
application accesses mapped address
              │
              ▼
is corresponding page in memory?
     │
     ├── yes → memory access continues
     │
     └── no  → page fault
               └── OS loads file page into page cache
```

The application can then decode offsets and records from the mapped byte slice
without calling `ReadAt` for every piece.

### Benefits

```text
fewer explicit read/write syscalls
random access through normal byte indexing
operating system manages cached pages and eviction
simpler-looking parsing code
```

### Costs and risks

With `ReadAt`, an I/O error is returned through ordinary Go control flow:

```text
ReadAt → error → caller decides what to do
```

With mapped memory, an error encountered while faulting in a page cannot be
returned from a slice-index expression. On systems such as Linux, a bad mapped
access or storage failure may produce a fatal signal. That failure model may
be unacceptable for some databases.

Other practical concerns include mapping lifetime, unmapping, file truncation,
address-space use, and platform differences.

### Chapter choice

The 0602 implementation uses `ReadAt`:

```text
implemented: index + ReadAt + returned errors
discussed:   mmap as an alternative design
```

Do not add `mmap` merely to finish the 0602 code unless deliberately extending
beyond the exercise.

---

## 18. Complexity and Memory Use

Let:

```text
N = number of records
K = key length of the record being read
V = value length of the record being read
```

### Record by position

```text
offset-slot calculation: O(1)
number of ReadAt calls:   constant
payload copying:          O(K + V)
temporary payload memory: O(K + V)
```

The whole SSTable is not allocated in RAM.

### Seek

```text
binary-search comparisons: O(log N)
record reads:              O(log N)
```

Each comparison currently obtains a record through `index(mid)`. The amount of
payload read therefore also depends on the middle records' key/value sizes.

### Full iteration

```text
records visited: O(N)
data read:       proportional to encoded record data
live iterator payload: one current record
```

Operating-system caching can make repeated or nearby accesses cheaper, but it
does not change the algorithmic lookup count.

---

## 19. Implementation Order for 0602

A clean implementation order follows the dependency graph:

```text
1. Add file.nkeys
   │
   └── set it after writing the SSTable

2. Implement index(pos)
   │
   ├── read offset slot
   ├── read length header
   └── read and split payload

3. Add SortedFileIter
   │
   ├── Valid, Key, Val
   ├── loadCurrent
   ├── Next
   └── Prev

4. Implement SortedFile.Size and SortedFile.Iter

5. Implement findPos(target)
   └── lower-bound binary search using index(mid)

6. Implement Seek(target)
   └── find position, construct iterator, load current record

7. Run the chapter test
```

Dependency tree:

```text
index(pos)
├── iterator loadCurrent
│   ├── Iter
│   ├── Next
│   └── Prev
│
└── findPos
    └── Seek
```

`index(pos)` is the foundation. Once one record can be reliably loaded by
position, iteration and binary search become small coordination layers.

---

## 20. What the Chapter Test Demonstrates

The 0602 test uses:

```text
keys: [x, y]
vals: [1, 234]
```

It verifies four different properties.

### 1. The file is still encoded correctly

The test compares the entire file with the expected 46 bytes. This protects
the 0601 serialization format while the read behavior is added.

### 2. `Size()` reports two records

```text
sf.Size() == 2
```

This confirms that `file.nkeys` is set correctly.

### 3. `Iter()` reads both records in order

```text
first:  x → 1
second: y → 234
then:   invalid iterator
```

This exercises `Iter`, `loadCurrent`, `index`, and `Next` together.

### 4. `Seek("xx")` returns `y`

```text
x < xx < y
```

This confirms lower-bound behavior rather than exact-match-only behavior.

The test is an end-to-end path:

```text
sorted input
   │
   ▼
CreateFromSorted
   │
   ▼
encoded SSTable
   │
   ├──► Size
   ├──► Iter + Next
   └──► Seek("xx")
```

---

## 21. Common Misunderstandings

### “`ReadAt(buf[:], 16)` reads buffer position 16.”

No. `buf` has only positions `0..7`. The number `16` is an absolute position
inside the file. File bytes `16..23` are copied into `buf[0..7]`.

### “Position 1 means file byte 1.”

No. `pos` is a logical record number. The code first converts it to an offset
slot location using `8 + 8*pos`.

### “For `index(1)`, byte 16 is where KV1 begins.”

No. Byte 16 is where `offset[1]` is stored. Decoding that slot gives `34`,
which is where KV1 begins.

### “The offset points directly to the key.”

No. It points to the record's length header. The key starts eight bytes later.

```text
recordOffset ──► keyLen | valLen | key | value
                                  ▲
                                  └── recordOffset + 8
```

### “Reusing `buf` loses the decoded offset.”

No. Before reusing `buf`, the code decodes the bytes into the independent Go
variable `offset`. Overwriting `buf` does not overwrite that integer variable.

### “`index(pos)` loads the complete SSTable.”

No. It reads one offset, one record header, and one record payload.

### “`Seek` returns only exact matches.”

No. It returns the first key greater than or equal to the target. Callers can
compare the returned key with the target when exact-match behavior is needed.

### “Binary search eliminates disk I/O.”

No. It reduces the number of records inspected from `O(N)` to `O(log N)`, but
each inspected middle record still has to be obtained from the file or cache.

### “A false `Valid()` means an I/O error occurred.”

No. `Valid() == false` normally means the iterator is before the first record
or after the last. I/O failures are returned separately as errors.

### “`mmap` loads the entire SSTable into physical RAM immediately.”

Not necessarily. The OS generally maps a virtual address range and faults file
pages into memory as they are accessed.

### “0602 requires implementing `mmap`.”

No. The chapter discusses it as an alternative. The implementation uses
`ReadAt` so errors can be returned normally.

---

## 22. Review Questions

### 1. Why is an offset table needed?

Records have variable lengths, so their starting positions cannot be computed
using one fixed record size. Fixed-size offset entries provide direct access
to each record's absolute position.

### 2. How do you find the offset slot for record position `pos`?

```text
8 + 8*pos
```

The first eight bytes store `nkeys`, and each earlier offset occupies eight
bytes.

### 3. For two records, why does the record region begin at byte 24?

```text
8-byte nkeys + 2 × 8-byte offsets = 24 bytes
```

### 4. What is the difference between offset-slot position and record offset?

The slot position tells the database where to read an encoded offset. The
decoded record offset tells it where the actual record header begins.

For record 1 in the mock file:

```text
offset-slot position = 16
record offset        = 34
```

### 5. What does `ReadAt(buf[:], 16)` do?

It copies `len(buf)` bytes from the file beginning at byte 16 into `buf`
beginning at `buf[0]`.

### 6. Why are there separate byte-reading and integer-decoding operations?

`ReadAt` transfers raw bytes. `binary.LittleEndian.Uint64` or `Uint32`
interprets those bytes as a number using the file's selected byte order.

### 7. Why does `index(pos)` first read only eight bytes from the record?

Those eight bytes contain `keyLen` and `valLen`. The decoder needs those
lengths before it knows how much payload memory to allocate and read.

### 8. Why is the payload read from `recordOffset + 8`?

The first eight record bytes are the two four-byte lengths. The raw key starts
immediately after that header.

### 9. Why can key and value share one allocation?

They are adjacent in the encoded payload. One `data` slice can hold both, and
two slice views can divide it at `keyLen` without another copy.

### 10. Why does the file iterator store `key` and `val`?

The current record must be loaded from disk. Caching it on the iterator lets
`Key()` and `Val()` return the current record after `Iter`, `Seek`, `Next`, or
`Prev` loads it.

### 11. Why can `Next()` and `Prev()` return errors?

Moving to a valid new position triggers file reads through `index(pos)`, and
disk I/O or decoding can fail.

### 12. What does `Seek(target)` return when the target is absent?

It returns an iterator at the first key greater than the target. If the target
is greater than every key, the returned iterator is invalid at position
`nkeys`.

### 13. Why is lower-bound search useful for range queries?

It positions the iterator directly at the first possible record in the range,
after which `Next()` scans forward in sorted order.

### 14. Why is binary search `O(log N)` but still potentially random I/O?

It halves the logical search range each time, but the chosen middle positions
can be far apart in the file.

### 15. Why is `bufio.Reader` less directly suited to binary search?

It is designed to benefit sequential reads. Binary search jumps among file
positions and may not consume the nearby bytes placed in a sequential buffer.

### 16. What does `mmap` provide?

It maps file pages into virtual memory, allowing the application to access
file contents through memory addresses while the OS page cache loads and
evicts pages.

### 17. What is the important error-handling tradeoff of `mmap`?

Explicit `ReadAt` calls can return ordinary errors. Failures during mapped
memory access may arrive as process-level signals instead of normal Go errors.

### 18. What must be true before `index(pos)` is safe to call?

The position must be within `[0, nkeys)`, the file must be open, and its
metadata and record bytes must be structurally valid enough for the required
reads and decodes.

---

## 23. Implementation Map

```text
SortedFile.CreateFromSorted(input)
│
├── create and write SSTable as in 0601
├── verify emitted count
├── file.nkeys = emitted count
└── sync

SortedFile.index(pos)
│
├── ReadAt(offset slot, 8 + 8*pos)
├── decode record offset
├── validate record-region boundary
├── ReadAt(length header, record offset)
├── decode keyLen and valLen
├── allocate payload
├── ReadAt(payload, record offset + 8)
└── split and return key/value

SortedFile.Iter()
│
├── create iterator at pos 0
└── loadCurrent()
    └── index(0)

SortedFileIter.Next/Prev()
│
├── update pos
└── loadCurrent()
    └── index(pos), if valid

SortedFile.Seek(target)
│
├── findPos(target)
│   ├── binary search [0, nkeys)
│   └── index(mid) for each comparison
├── create iterator at result position
└── loadCurrent(), if valid
```

---

## Crucial Takeaways

- The offset table turns variable-length records into an on-disk array that
  supports direct record-by-position access.
- `pos` is a logical record number; `8 + 8*pos` is the location of its offset
  entry; the decoded offset is the location of its record header.
- `ReadAt(buf, offset)` copies bytes from the file into `buf`, the reverse data
  direction of `WriteAt`.
- Reading one record takes an offset read, a length-header read, and a payload
  read; it does not require loading the whole SSTable.
- One payload allocation can be split into key and value slice views.
- A file-backed iterator stores a logical position plus the currently loaded
  key and value, and movement can return I/O errors.
- `Seek` is a lower-bound binary search: it returns the first key greater than
  or equal to the target.
- Lower-bound behavior supports exact lookups and efficient range-scan starts.
- Returning iterator interfaces keeps consumers independent of whether sorted
  data comes from RAM or an SSTable.
- `bufio.Reader` mainly helps sequential access; binary search is random
  access and needs a page-cache strategy for effective caching.
- `mmap` exposes file-backed pages as memory and leverages the OS page cache,
  but it changes the error model and is discussed rather than implemented in
  this chapter.
