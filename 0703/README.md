# Chapter 0703: Multiple Levels

## The Main Idea

Chapter 0703 changes the database from having **one SSTable** to having a
**list of SSTables**.

Before this chapter, the database looked roughly like this:

```text
MemTable (new changes)
        +
one SSTable (older data)
```

After Chapter 0703, it looks like this:

```text
MemTable             newest data
SSTable level 0
SSTable level 1
SSTable level 2      oldest data
```

Each compaction turns the current MemTable into a new SSTable and inserts it
at the front of the list. It does **not** merge or delete the older SSTables
yet. Merging levels is the job of Chapter 0704.

---

## How the 07xx Chapters Connect

### 0700: Introduce the LSM-tree plan

An LSM tree accepts writes in a small, fast, mutable area and later moves
those writes into immutable sorted files:

```text
writes -> WAL + MemTable -> SSTables -> larger SSTable levels
```

The newer levels must take priority because they contain more recent values.

### 0701: Store metadata atomically

The database needs durable metadata describing its SSTables. Chapter 0701
adds two metadata files:

```text
meta0: version 8 + data + checksum
meta1: version 9 + data + checksum
```

The newest valid version wins. Using two copies means that one damaged or
partially written metadata file does not destroy the database's file list.

### 0702: Give SSTables dynamic names

Every compaction creates a uniquely named file:

```text
sstable_1
sstable_2
sstable_3
```

Metadata records which one is active:

```go
type KVMetaData struct {
	Version uint64
	SSTable string
}
```

Chapter 0702 still has only one active SSTable. A new compaction replaces the
old active SSTable in metadata, after which the old file can be removed.

### 0703: Remember multiple SSTables

Now metadata stores an ordered list instead of one filename:

```go
type KVMetaData struct {
	Version  uint64
	SSTables []string
}
```

For example:

```go
KVMetaData{
	Version: 4,
	SSTables: []string{
		"sstable_4", // newest
		"sstable_3",
		"sstable_2", // oldest
	},
}
```

The order is meaningful: the first file has priority over later files when
the same key exists in more than one level.

---

## 1. One `SortedFile` Becomes a Slice

Chapter 0702 stores one SSTable:

```go
type KV struct {
	main SortedFile
}
```

Chapter 0703 stores multiple SSTables:

```go
type KV struct {
	main []SortedFile
}
```

The slice is ordered from newest to oldest:

```text
kv.main[0] -> newest SSTable
kv.main[1] -> next older SSTable
kv.main[2] -> oldest SSTable
```

Example:

```text
MemTable:   c = 30
main[0]:    b = 20       sstable_4
main[1]:    a = 10       sstable_3
```

A query must consider all three sources as one logical database.

---

## 2. Opening Multiple SSTables

When the database opens, it reads the ordered filename list from metadata and
opens every file:

```go
func (kv *KV) openSSTable() error {
	meta := kv.meta.Get()
	kv.version = meta.Version
	kv.main = kv.main[:0]

	for _, sstable := range meta.SSTables {
		filename := path.Join(kv.Options.Dirpath, sstable)
		file := SortedFile{FileName: filename}

		if err := file.Open(); err != nil {
			return err
		}

		kv.MultiClosers = append(kv.MultiClosers, &file)
		kv.main = append(kv.main, file)
	}

	return nil
}
```

Suppose metadata contains:

```text
["sstable_4", "sstable_3", "sstable_2"]
```

and `Dirpath` is `test_db`. The loop opens:

```text
test_db/sstable_4
test_db/sstable_3
test_db/sstable_2
```

It appends the files in that same order, preserving their newest-to-oldest
priority.

---

## 3. Reads Merge Every Level

Previously, `Seek` merged only two sources:

```go
MergedSortedKV{&kv.mem, &kv.main}
```

Now it builds the level list dynamically:

```go
func (kv *KV) Seek(key []byte) (SortedKVIter, error) {
	levels := MergedSortedKV{&kv.mem}

	for i := range kv.main {
		levels = append(levels, &kv.main[i])
	}

	iter, err := levels.Seek(key)
	if err != nil {
		return nil, err
	}

	return filterDeleted(iter)
}
```

The final priority is:

```text
1. MemTable
2. kv.main[0]
3. kv.main[1]
4. kv.main[2]
5. ...
```

### Example: the same key in several levels

```text
MemTable:       user:1 = "newest"
sstable_4:      user:1 = "newer"
sstable_3:      user:1 = "old"
```

The merged iterator returns:

```text
user:1 = "newest"
```

If the MemTable does not contain the key, `sstable_4` wins. The source that
appears earlier in `MergedSortedKV` always represents the newer state.

---

## 4. Why SSTables Must Store Deletions

This is the most important correctness change in Chapter 0703.

Suppose an old SSTable contains:

```text
sstable_2: account:7 = "active"
```

The user then deletes `account:7`. The MemTable records a tombstone:

```text
MemTable: account:7 = DELETED
```

A tombstone is a record saying, “This key was deliberately deleted.” It must
hide older values of that key.

Now compact the MemTable into `sstable_3`. If the new SSTable discards the
tombstone, the database will see the old value again:

```text
sstable_3: no account:7
sstable_2: account:7 = "active"
                              ^
                              old value incorrectly returns
```

Therefore, the new top-level SSTable must preserve the deletion:

```text
sstable_3: account:7 = DELETED
sstable_2: account:7 = "active"

query result: not found
```

The tombstone cannot be safely removed until the engine merges it with every
older level that may contain the key. Chapter 0704 handles that later.

---

## 5. The SSTable Record Format Changes

Earlier SSTables stored only key and value lengths:

```text
[ key length | value length | key bytes | value bytes ]
```

Chapter 0703 adds one deletion byte:

```text
[ key length | value length | deleted | key bytes | value bytes ]
    4 bytes       4 bytes      1 byte
```

The flag means:

```text
deleted = 0 -> normal key/value record
deleted = 1 -> tombstone
```

The disk iterator must now remember and expose that flag:

```go
type SortedFileIter struct {
	file    *SortedFile
	pos     int
	key     []byte
	val     []byte
	deleted bool
}

func (iter *SortedFileIter) Deleted() bool {
	return iter.deleted
}
```

This makes a disk SSTable behave like the MemTable: both can return normal
records or tombstones through the same `SortedKVIter` interface.

---

## 6. Compaction Now Creates a New Top Level

In Chapter 0702, compaction merged the MemTable with the existing SSTable and
replaced that SSTable.

Chapter 0703 changes the operation:

```text
MemTable only -> new SSTable -> insert at main[0]
```

The older SSTables remain untouched.

The important parts are:

```go
func (kv *KV) Compact() error {
	kv.version++
	sstable := fmt.Sprintf("sstable_%d", kv.version)
	filename := path.Join(kv.Options.Dirpath, sstable)
	file := SortedFile{FileName: filename}

	if err := file.CreateFromSorted(&kv.mem); err != nil {
		_ = os.Remove(filename)
		return err
	}

	meta := kv.meta.Get()
	meta.Version = kv.version
	meta.SSTables = slices.Insert(meta.SSTables, 0, sstable)

	if err := kv.meta.Set(meta); err != nil {
		_ = file.Close()
		return err
	}

	kv.main = slices.Insert(kv.main, 0, file)
	kv.mem.Clear()
	return kv.log.Truncate()
}
```

Notice this input:

```go
file.CreateFromSorted(&kv.mem)
```

It writes only the MemTable. It no longer merges `kv.mem` with `kv.main`.

### Example: three compactions

Start with no SSTables:

```text
metadata.SSTables = []
```

After the first compaction:

```text
metadata.SSTables = [sstable_1]
```

After the second compaction:

```text
metadata.SSTables = [sstable_2, sstable_1]
```

After the third compaction:

```text
metadata.SSTables = [sstable_3, sstable_2, sstable_1]
```

Each new file is inserted at index `0` because it contains the newest data.

---

## 7. Complete Write and Read Example

Consider these operations:

```text
SET a = 1
COMPACT
SET a = 2
SET b = 3
COMPACT
DEL a
COMPACT
```

The physical levels become:

```text
MemTable:     empty

sstable_3:    a = DELETED    newest
sstable_2:    a = 2
              b = 3
sstable_1:    a = 1          oldest
```

The logical database is:

```text
a -> not found
b -> 3
```

For `a`, the tombstone in `sstable_3` wins over both older values. For `b`,
the first level containing it is `sstable_2`, so the database returns `3`.

This is the central LSM-tree rule:

> When the same key appears in multiple levels, the newest level wins.

---

## 8. What Chapter 0703 Does Not Do Yet

The number of SSTables grows after every compaction:

```text
sstable_1
sstable_2
sstable_3
sstable_4
...
```

More files mean that a read may need to check more levels. Chapter 0703
creates the multi-level representation but does not control its growth.

Chapter 0704 will:

- decide when the WAL should become a new SSTable;
- decide when adjacent SSTable levels are too large;
- merge adjacent levels;
- remove tombstones when merging into the final level; and
- introduce a growth factor that balances read cost against write cost.

So Chapter 0703 is an intermediate but necessary step:

```text
0702: replace one SSTable safely
          |
          v
0703: keep many ordered SSTables
          |
          v
0704: merge and control those SSTable levels
```

---

## Implementation Checklist

- Change `KV.main` from `SortedFile` to `[]SortedFile`.
- Change `KVMetaData.SSTable` to `KVMetaData.SSTables []string`.
- Open every SSTable listed in metadata.
- Build reads from the MemTable followed by every SSTable level.
- Add a deletion byte to SSTable records.
- Make `SortedFileIter.Deleted()` return the stored deletion flag.
- Make `Compact()` write only the MemTable into a new SSTable.
- Insert the new filename at the front of metadata's SSTable list.
- Insert the new open file at the front of `kv.main`.
- Clear the MemTable and truncate the WAL only after metadata is updated.
- Keep all older SSTables; Chapter 0704 will merge them.

## Source

Based on Chapter 0703, “Multiple Levels,” printed page 79 of
`/home/morgankim/Documents/ebooks/db_in_45_steps_go.pdf`.
