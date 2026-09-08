# Chapter 0704: Merge Levels

## The Main Idea

Chapter 0703 lets the database keep many SSTables, but every compaction adds
another file:

```text
MemTable
sstable_7     newest
sstable_6
sstable_5
...
sstable_1     oldest
```

This is correct, but reads become slower as the list grows. Chapter 0704 turns
that list into a real leveled LSM-tree by merging adjacent SSTables when their
sizes are too similar.

```text
small, new levels
        |
        v
larger, older levels
```

The target shape is approximately exponential:

```text
level 0:   1 unit
level 1:   2 units
level 2:   4 units
level 3:   8 units
```

With exponentially growing levels, the database keeps only `O(log N)` levels
for `N` keys.

---

## How 0703 Becomes 0704

Chapter 0703 has one compaction operation:

```text
MemTable -> new SSTable at main[0]
```

Chapter 0704 splits that work into two operations:

```text
compactLog:
MemTable -> new SSTable at main[0]

compactSSTable:
main[i] + main[i+1] -> one replacement SSTable
```

`Compact()` becomes the coordinator. It may flush the MemTable, merge one or
more pairs of SSTables, or do nothing when all levels are within their size
limits.

---

## 1. The Two LSM-Tree Parameters

Chapter 0704 adds two options:

```go
type KVOptions struct {
	Dirpath string

	LogShreshold int
	GrowthFactor float32
}
```

`LogShreshold` is spelled this way in the chapter's code. It means the maximum
number of keys allowed in the MemTable before `Compact()` flushes it to an
SSTable.

`GrowthFactor` controls how much larger an older level should be than the newer
level above it.

`Open()` supplies safe defaults:

```go
if kv.Options.LogShreshold <= 0 {
	kv.Options.LogShreshold = 1000
}
if kv.Options.GrowthFactor < 2.0 {
	kv.Options.GrowthFactor = 2.0
}
```

The minimum growth factor is `2`, so levels grow at least geometrically rather
than forming a long list of similarly sized files.

### The trade-off

A larger growth factor forces older levels to become larger before they are
left alone:

- There are fewer levels, so reads check fewer SSTables.
- Merges happen more aggressively, so keys may be rewritten more often.
- Read performance improves at the cost of write throughput.

Rewriting the same logical key during several merges is called **write
amplification**.

---

## 2. `Compact()` Coordinates All Merges

The public compaction method first checks the MemTable, then checks adjacent
SSTable levels:

```go
func (kv *KV) Compact() error {
	if kv.mem.Size() >= kv.Options.LogShreshold {
		if err := kv.compactLog(); err != nil {
			return err
		}
	}

	for i := 0; i < len(kv.main)-1; i++ {
		if kv.shouldMerge(i) {
			if err := kv.compactSSTable(i); err != nil {
				return err
			}
			i--
			continue
		}
	}

	return nil
}
```

The `i--` is important. After levels `i` and `i+1` become one file, that new
file must be compared with its next older neighbor. This allows one call to
`Compact()` to cascade through several levels.

```text
[1] [1] [2]
  merge
    |
    v
  [2] [2]
    merge again
       |
       v
      [4]
```

Without rechecking the same position, the second required merge would wait for
a later call.

---

## 3. Deciding When Two Levels Should Merge

Levels are stored newest to oldest:

```text
main[0] -> newest and usually smallest
main[1] -> older and larger
main[2] -> older and larger again
```

The decision uses the number of keys as an estimate of level size:

```go
func (kv *KV) shouldMerge(idx int) bool {
	cur := kv.main[idx].EstimatedSize()
	next := kv.main[idx+1].EstimatedSize()

	return float32(cur)*kv.Options.GrowthFactor >= float32(cur+next)
}
```

For a growth factor of `2`, the equation simplifies to:

```text
2 * current >= current + next
current >= next
```

So two adjacent levels merge when the older level is not larger than the newer
level.

Examples with `GrowthFactor = 2`:

```text
current = 2, next = 1 -> merge
current = 2, next = 2 -> merge
current = 2, next = 4 -> do not merge
```

The last case already has the desired exponential shape.

---

## 4. Flushing the MemTable

The old 0703 `Compact()` becomes `compactLog()`:

```text
WAL + MemTable -> new top-level SSTable
```

Its main steps are:

1. Reserve a new monotonically increasing version and filename.
2. Write the MemTable to that new SSTable.
3. Insert the filename at the front of metadata.
4. Atomically store the new metadata.
5. Insert the open file at `main[0]`.
6. Clear the MemTable and truncate the WAL.

The ordering protects durability. The MemTable and WAL are not cleared until
the SSTable exists and durable metadata points to it.

### Dropping tombstones when this becomes the only level

If there are no older SSTables, a deletion marker cannot be hiding an older
value. It is safe to remove it immediately:

```go
m := SortedKV(&kv.mem)
if len(kv.main) == 0 {
	m = NoDeletedSortedKV{m}
}
```

If older levels do exist, tombstones must remain in the new SSTable.

---

## 5. Merging Two SSTables

`compactSSTable(level)` merges two adjacent levels:

```text
main[level]       newer
       +
main[level+1]     older
       |
       v
one new sorted SSTable
```

The merge input keeps the newer file first:

```go
m := SortedKV(MergedSortedKV{
	&kv.main[level],
	&kv.main[level+1],
})
```

This ordering is a correctness rule. If both levels contain the same key, the
value from `main[level]` wins because it is newer.

### Example

```text
newer level:  a=9, c=3
older level:  a=1, b=2, d=4
```

The merged output is:

```text
a=9, b=2, c=3, d=4
```

The old value `a=1` disappears because the merge iterator emits only the
newest version of a duplicate key.

---

## 6. Updating Metadata Before Removing Old Files

The result is written to a unique new SSTable. Metadata then replaces the two
old filenames with the new filename:

```go
meta.SSTables = slices.Replace(
	meta.SSTables,
	level,
	level+2,
	sstable,
)
```

For example:

```text
before: [sstable_8, sstable_6, sstable_3]
merge indexes 1 and 2
after:  [sstable_8, sstable_9]
```

The safe order is:

```text
1. create and sync sstable_9
2. atomically store metadata referencing sstable_9
3. replace the two levels in memory
4. remove sstable_6 and sstable_3
```

Old files must not be removed before metadata is durable. If metadata storage
fails, the on-disk state may be uncertain, so keeping all files is safer than
deleting a file that recovery might need.

The version increases for every attempted new SSTable, including a failed
attempt. This prevents a later compaction from accidentally reusing a filename
whose earlier metadata update may actually have reached disk.

---

## 7. When Tombstones Can Be Removed

A tombstone means that a key was deliberately deleted:

```text
newer level: account:7 = DELETED
older level: account:7 = "active"
```

Removing the tombstone too early would expose the older value again.

The rule is:

> Keep a tombstone while any older level could still contain that key. Remove
> it only when producing the final level.

While merging levels `level` and `level+1`, they are the final two levels when:

```go
len(kv.main) == level+2
```

In that case, the merged result becomes the bottom level, so tombstones can be
filtered:

```go
if len(kv.main) == level+2 {
	m = NoDeletedSortedKV{m}
}
```

`NoDeletedSortedKV` is an adapter around any `SortedKV`:

```go
type NoDeletedSortedKV struct {
	SortedKV
}

func (kv NoDeletedSortedKV) Iter() (iter SortedKVIter, err error) {
	if iter, err = kv.SortedKV.Iter(); err != nil {
		return nil, err
	}
	return NoDeletedIter{iter}, nil
}
```

This reuses the existing deletion-filtering iterator instead of adding special
logic to the SSTable writer.

### Tombstone example

Before the bottom-level merge:

```text
newer: a=DELETED, c=3
older: a=1, b=2
```

The merge first resolves duplicate keys:

```text
a=DELETED, b=2, c=3
```

Because there is no older level left, the tombstone is then removed:

```text
b=2, c=3
```

The deleted value cannot return because every possible older copy participated
in the merge.

---

## 8. Complete Growth Example

Assume:

```text
LogShreshold = 1
GrowthFactor = 2
```

Every single-key MemTable can be flushed. The levels evolve like a binary
counter:

```text
write a, compact:  [1]
write b, compact:  [1, 1] -> [2]
write c, compact:  [1, 2]
write d, compact:  [1, 1, 2] -> [2, 2] -> [4]
write e, compact:  [1, 4]
```

Square brackets show SSTable key counts, newest on the left. Equal-sized
levels repeatedly combine into the next larger level.

In real data, duplicate keys and tombstones mean merged counts are estimates,
not always exact sums. For example, two files with the same key produce only
one copy of that key.

---

## 9. Complexity and Why This Helps

Without merging, one SSTable can be created per flush, so the number of files
can grow linearly with the number of writes.

With exponential level growth:

- The number of levels is `O(log N)`.
- A key moves downward through at most `O(log N)` levels.
- A point lookup checks at most `O(log N)` sorted structures.
- Compaction work is spread across many writes in the average-cost analysis.

One individual merge can still be large and slow. The complexity is amortized;
it does not guarantee that every call to `Compact()` is fast.

---

## 10. What Chapter 0704 Does Not Do

This chapter demonstrates the core merge policy, but it is not yet a
production compaction system.

It does not:

- call `Compact()` automatically after every write;
- compact in a background goroutine;
- spread one large merge across several operations;
- limit write speed when compaction falls behind; or
- split one level into several SSTable files.

The caller must still trigger `Compact()` explicitly. This avoids introducing
concurrency before the database has the synchronization needed to safely
replace files while readers may be using them.

Keeping one file per level is also simple but can temporarily require roughly
double the level's disk space during a merge. Real systems commonly split a
level into multiple files and merge only the overlapping pieces.

---

## Implementation Checklist

- Add `LogShreshold` and `GrowthFactor` to `KVOptions`.
- Default the log threshold to `1000` when it is non-positive.
- Clamp the growth factor to at least `2.0`.
- Make `Compact()` flush only when the MemTable reaches the threshold.
- Move the 0703 flush behavior into `compactLog()`.
- Add `shouldMerge()` using the exponential size rule.
- Scan adjacent levels and allow cascading merges.
- Add `compactSSTable(level)` to merge two neighboring SSTables.
- Preserve newest-before-oldest priority during the merge.
- Replace both filenames atomically in metadata.
- Update in-memory levels only after metadata succeeds.
- Remove old SSTable files only after the metadata switch succeeds.
- Preserve tombstones while older levels remain.
- Drop tombstones when producing the final level.
- Keep compaction explicitly triggered for now.

---

## Review Questions

1. Why does 0703 become slower as more compactions occur?
2. What do `LogShreshold` and `GrowthFactor` control?
3. With a growth factor of `2`, when does `shouldMerge()` return true?
4. Why does the compaction loop recheck the same level after a merge?
5. Why must the newer SSTable appear first in `MergedSortedKV`?
6. Why is it unsafe to remove a tombstone from a non-final level?
7. Why must metadata be updated before old SSTable files are removed?
8. Why does exponential growth keep the number of levels logarithmic?
9. What is write amplification, and how does the growth factor affect it?
10. Why does this chapter avoid automatically running large merges on writes?

---

## Quick Review

```text
0703:
flushes create an ever-growing list of SSTables

0704:
MemTable reaches threshold
        |
        v
new top-level SSTable
        |
        v
compare adjacent level sizes
        |
        v
merge until levels grow exponentially
        |
        v
drop tombstones only at the bottom
```

The central rule to remember is:

> New data enters at the top, adjacent levels merge downward, and deletion
> markers disappear only when no older data remains.

## Source

Based on Chapter 0704, “Merge Levels,” printed pages 80–81 of
`/home/morgankim/Documents/ebooks/db_in_45_steps_go.pdf`, cross-checked against
`db_solution/0704`.
