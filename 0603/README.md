# Chapter 0603: Refactor Code

## Overview

Chapter 0603 adds no new database feature. It reorganizes the in-memory sorted
key/value data into its own type so that the database can later query both:

```text
MemTable (in memory) ─┐
                      ├──► merged query results
SSTable (on disk) ────┘
```

Before this chapter, `KV` directly owns two parallel slices:

```go
type KV struct {
	log  Log
	keys [][]byte
	vals [][]byte
}
```

After the refactor, `KV` delegates its in-memory operations to a
`SortedArray`:

```go
type KV struct {
	log Log
	mem SortedArray
}

type SortedArray struct {
	keys [][]byte
	vals [][]byte
}
```

The important design change is ownership: `KV` should no longer manipulate
the `keys` and `vals` slices directly.

---

## Why This Refactor Is Needed

Chapter 0602 made an SSTable queryable through the `SortedKV` and
`SortedKVIter` interfaces. The in-memory data must implement the same
interface before both sources can be treated uniformly.

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

Once `SortedArray` implements these interfaces, `SortedFile` does not need to
know whether its input came from the main database or from some other sorted
source.

### An Easy Mental Model

Think of the types as different jobs in a library:

```text
KV                 = the librarian managing everything
SortedArray        = books arranged on a desk
SortedArrayIter    = a finger moving across books on the desk
SortedFile         = books stored in an archive cabinet
SortedFileIter     = a finger moving through books in the cabinet
```

The container holds all the data. An iterator is a separate cursor that
remembers one current position within that data.

### `SortedArray` and `SortedArrayIter`

`SortedArray` contains all the current in-memory records:

```text
SortedArray

index        0           1           2
         ┌─────────┬──────────┬─────────┐
keys     │ "apple" │ "banana" │ "pear"  │
vals     │ "red"   │ "yellow" │ "green" │
         └─────────┴──────────┴─────────┘
```

`SortedArrayIter` points at one position in that array:

```text
SortedArrayIter
      pos = 1
         │
         ▼
keys  ["apple", "banana", "pear"]
vals  ["red",   "yellow", "green"]
```

At this position, `Key()` returns `"banana"` and `Val()` returns `"yellow"`.
Calling `Next()` moves the cursor rather than moving or copying the records:

```text
before Next()                   after Next()

             ▼                            ▼
["apple", "banana", "pear"]   ["apple", "banana", "pear"]
          pos = 1                      pos = 2
```

`Iter()` does not loop through the data itself. It creates a new iterator at
position zero. The caller then uses `Valid`, `Key`, `Val`, `Next`, and `Prev`
to control the traversal.

### Why `KVIterator` Becomes `SortedArrayIter`

Previously, the two sorted slices belonged directly to `KV`:

```text
KV
├── keys
├── vals
└── KVIterator
```

After the refactor, they belong to `SortedArray`:

```text
KV
└── mem: SortedArray
         ├── keys
         ├── vals
         └── SortedArrayIter
```

The new name accurately describes what the iterator traverses. It does not
traverse the entire database; it traverses only the in-memory sorted array.
This distinction matters when `KV` contains multiple data sources:

```text
KV
├── mem: SortedArray
└── disk: SortedFile
```

A future database query may need to combine an iterator from each source:

```text
SortedArrayIter ─┐
                 ├──► merged database results
SortedFileIter ──┘
```

### `SortedArray` Versus `SortedFile`

Both types contain sorted key/value records, but they store and access them
differently:

| Property | `SortedArray` | `SortedFile` |
| --- | --- | --- |
| Location | RAM | Disk |
| Representation | Parallel `keys` and `vals` slices | Encoded SSTable file |
| Modification | Can insert, update, and delete | Normally immutable |
| Access | Direct slice access | Reads offsets and record bytes |
| Iterator | `SortedArrayIter` | `SortedFileIter` |
| Lifetime | Rebuilt when the database opens | Remains after restart |

Although their internal representations differ, they expose the same logical
view:

```text
SortedArray              SortedFile
───────────              ──────────
apple  → red             apple  → red
banana → yellow          banana → yellow
pear   → green           pear   → green
```

That shared view is expressed through the interfaces:

```text
SortedArray     ──implements──► SortedKV
SortedFile      ──implements──► SortedKV

SortedArrayIter ──implements──► SortedKVIter
SortedFileIter  ──implements──► SortedKVIter
```

Calling code can therefore request sorted data without caring whether that
data is in memory or on disk.

### How `CreateFromSorted` Uses the Abstraction

`SortedFile.CreateFromSorted` accepts the interface rather than a particular
concrete input type:

```go
func (file *SortedFile) CreateFromSorted(kv SortedKV) error
```

It only needs the operations promised by `SortedKV`:

```go
size := kv.Size()
iter, err := kv.Iter()
```

It does not need direct access to `SortedArray.keys` or `SortedArray.vals`.
The flow is:

```text
SortedArray
    │
    │ Size() and Iter()
    ▼
SortedFile.CreateFromSorted()
    │
    ▼
SSTable written to disk
```

The same method can accept any other type that fulfills the `SortedKV`
contract.

### Why the Iterator Is Separate from the Collection

Separating the iterator allows multiple independent cursors over the same
data:

```text
SortedArray: ["apple", "banana", "pear"]

iterator 1      ▼
             "apple"

iterator 2                          ▼
                                "pear"
```

If the current position lived inside `SortedArray`, every user would share
one position and moving one scan would disturb the others. Each iterator
instead owns its own `pos` while referring to the same underlying records.

---

## Implementation Plan

### 1. Create `sorted_array.go`

Move the parallel `keys` and `vals` slices and their operations out of
`kv.go`. `SortedArray` needs these methods:

```go
func (arr *SortedArray) Size() int
func (arr *SortedArray) Key(i int) []byte
func (arr *SortedArray) Clear()
func (arr *SortedArray) Push(key []byte, val []byte)
func (arr *SortedArray) Pop()

func (arr *SortedArray) Get(key []byte) (val []byte, ok bool, err error)
func (arr *SortedArray) Set(key []byte, val []byte) (updated bool, err error)
func (arr *SortedArray) Del(key []byte) (deleted bool, err error)

func (arr *SortedArray) Iter() (SortedKVIter, error)
func (arr *SortedArray) Seek(key []byte) (SortedKVIter, error)
```

Keep the two slices synchronized at all times:

```text
keys[0] belongs to vals[0]
keys[1] belongs to vals[1]
keys[n] belongs to vals[n]
```

An insertion or deletion must update both slices at the same index.

### 2. Rename the iterator

Rename `KVIterator` to `SortedArrayIter`. The iterator belongs to the sorted
array, not to the whole database.

```go
type SortedArrayIter struct {
	keys [][]byte
	vals [][]byte
	pos  int
}
```

It must implement:

```go
Valid()
Key()
Val()
Next()
Prev()
```

`Seek` is a lower-bound search. It returns an iterator positioned at the first
key greater than or equal to the requested key:

```text
stored keys:  ["a", "c", "f"]

Seek("c")  ──► "c"
Seek("d")  ──► "f"
Seek("z")  ──► invalid iterator at the end
```

### 3. Refactor `KV`

Replace direct slice access with calls to `kv.mem`:

```text
Old                         New
──────────────────────────────────────────────────
len(kv.keys)                kv.mem.Size()
kv.keys[i]                  kv.mem.Key(i)
clear both slices           kv.mem.Clear()
append key and value        kv.mem.Push(key, val)
remove the last pair        kv.mem.Pop()
binary-search in Get        kv.mem.Get(key)
insert/update slices        kv.mem.Set(key, val)
delete from both slices     kv.mem.Del(key)
```

The public `KV` API should continue to behave the same. The log remains the
durable record, while `SortedArray` represents the current in-memory state.

### 4. Return the iterator interface

Change `KV.Seek` from returning a concrete iterator:

```go
func (kv *KV) Seek(key []byte) (*KVIterator, error)
```

to returning the shared interface:

```go
func (kv *KV) Seek(key []byte) (SortedKVIter, error)
```

`RangedKVIter` must therefore store the interface as well:

```go
type RangedKVIter struct {
	iter SortedKVIter
	stop []byte
	desc bool
}
```

Do not dereference the iterator returned by `Seek`; assign the interface
directly.

---

## Test Change

The meaningful test change for this chapter is in `sorted_file_test.go`.
Earlier, that test declared a small temporary `SortedArray` implementation.
Chapter 0603 removes the test-only implementation because the real
`SortedArray` now belongs in production code.

The test then verifies that a real `SortedArray` satisfies `SortedKV`:

```go
err := sf.CreateFromSorted(&SortedArray{keys, vals})
```

If this compiles, `SortedArray` provides the methods required by the
`SortedKV` interface.

---

## Recommended Work Order

1. Add `sorted_array.go` with `SortedArray` and `SortedArrayIter`.
2. Implement the small structural methods: `Size`, `Key`, `Clear`, `Push`,
   and `Pop`.
3. Move `Get`, `Set`, `Del`, `Iter`, and `Seek` into `SortedArray`.
4. Replace every `kv.keys` and `kv.vals` access in `kv.go` with a `kv.mem`
   method call.
5. Update `KV.Seek`, `RangedKVIter`, and `Range` to use `SortedKVIter`.
6. Format and run the tests.

From the repository root:

```bash
gofmt -w 0603/*.go
go test ./0603
```

To catch accidental direct access left in `kv.go`:

```bash
rg 'kv\.(keys|vals)' 0603
```

That search should return no matches when the refactor is complete.

---

## Completion Checklist

- [ ] `SortedArray` lives in production code, preferably `sorted_array.go`.
- [ ] `KV` contains `mem SortedArray` instead of `keys` and `vals`.
- [ ] `KVIterator` is renamed to `SortedArrayIter`.
- [ ] `SortedArray` implements `SortedKV`.
- [ ] `SortedArrayIter` implements `SortedKVIter`.
- [ ] `KV` does not access the array fields directly.
- [ ] `KV.Seek` returns `SortedKVIter`.
- [ ] `RangedKVIter` stores `SortedKVIter`.
- [ ] `sorted_file_test.go` uses the production `SortedArray`.
- [ ] `go test ./0603` passes.

## Key Takeaway

This chapter separates the database API from one particular storage
representation. `KV` handles durability and database behavior;
`SortedArray` handles sorted in-memory data. That boundary prepares the code
for the next chapter, where multiple sorted sources must be merged.
