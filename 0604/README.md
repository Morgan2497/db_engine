# Chapter 0604: Merge Sorted Data

## Overview

Chapter 0604 prepares the database to query more than one sorted structure as
if they were a single sorted structure.

The future database will have at least:

```text
newer updates                         older durable state
┌──────────────────────┐             ┌──────────────────────┐
│ MemTable             │             │ SSTable              │
│ sorted in memory     │             │ sorted on disk       │
└──────────────────────┘             └──────────────────────┘
             │                                    │
             └──────────────┬─────────────────────┘
                            ▼
                   one merged sorted view
```

Later LSM-tree designs may have several SSTable levels:

```text
level[0]  newest / highest priority
level[1]
level[2]
level[3]  oldest / lowest priority
```

Each level is already sorted internally. The problem is therefore not to sort
unsorted input. The problem is to merge several sorted iterators while:

1. Returning keys in sorted order.
2. Returning each key only once.
3. Letting an upper, newer level win when duplicate keys exist.
4. Supporting both `Next()` and `Prev()`.
5. Remaining correct if iteration changes direction midway.
6. Propagating errors from any underlying iterator.

The core abstraction is:

```text
MergedSortedKV
└── contains k SortedKV sources
    └── Iter() creates one iterator for every source
        └── MergedSortedKVIter selects the current winning source
```

The key mental model is:

> Do not copy all levels into a new array. Keep one cursor in each sorted level
> and repeatedly select the smallest current key for forward iteration or the
> largest current key for backward iteration.

---

## 1. Connection to Chapters 0601–0603

### 0601: Build one sorted file

```text
sorted in-memory records ─────────► immutable SSTable
```

### 0602: Query one sorted file

```text
SSTable
├── index(pos)
├── Iter()
└── Seek(key)
```

### 0603: Give memory its own sorted type

```text
KV
└── mem SortedArray
    ├── Iter()
    └── Seek(key)
```

After 0603, memory and disk can both expose the same iterator behavior:

```text
SortedKV
├── SortedArray  — RAM
└── SortedFile   — disk
```

### 0604: Merge multiple sorted iterators

```text
SortedArray iterator ──┐
                       ├──► MergedSortedKVIter ──► one sorted stream
SortedFile iterator ───┘
```

Chapter 0604 implements the merge independently of the main `KV` integration.
Chapter 0605 will connect the log, MemTable, SSTable, deletion markers, and
compaction lifecycle.

---

## 2. Why More Than One Structure Exists

An LSM-style foreground write does not modify an existing SSTable. It records
the new state in the WAL and MemTable:

```text
existing SSTable                    incoming update
┌──────────────────┐               SET b = new
│ a → oldA         │                    │
│ b → oldB         │                    ▼
│ c → oldC         │               MemTable
└──────────────────┘               ┌──────────────────┐
                                   │ b → newB         │
                                   └──────────────────┘
```

A query must treat these as one logical database:

```text
logical view
├── a → oldA
├── b → newB     MemTable wins
└── c → oldC
```

This creates two requirements:

```text
ordering requirement
└── a, b, c must be returned in ascending order

version requirement
└── duplicate b must be returned once, using the newer value
```

The merge iterator handles both requirements without rewriting either source.

---

## 3. Priority: Earlier Levels Are Newer

`MergedSortedKV` is an ordered slice:

```go
type MergedSortedKV []SortedKV
```

Its order carries meaning:

```text
MergedSortedKV{
    level[0],  highest priority / newest
    level[1],
    level[2],  lowest priority / oldest
}
```

For the common two-source case:

```text
level[0] = MemTable      newer
level[1] = SSTable      older
```

Suppose both contain `b`:

```text
level[0]: b → newB
level[1]: b → oldB
```

The merged view must return:

```text
b → newB
```

It must not return both versions:

```text
b → newB
b → oldB       wrong: duplicate logical key
```

It must not let the older version win:

```text
b → oldB       wrong: stale value became visible
```

The implementation preserves priority by scanning levels from index `0`
upward and replacing the current winner only when a strictly smaller or
strictly larger key is found. An equal key does not replace the earlier
winner.

---

## 4. Point Queries and Range Queries Are Different

### Single-key lookup

For an exact lookup, the database can search levels from newest to oldest:

```text
GET b
  │
  ├── check level[0]
  │   ├── found → return immediately
  │   └── absent
  │
  ├── check level[1]
  └── continue downward as needed
```

The first exact match is authoritative because upper levels are newer.

### Range lookup

A range query cannot finish one level before starting the next:

```text
level[0]: b, d
level[1]: a, c, e
```

Reading all of level 0 first would produce:

```text
b, d, a, c, e     not sorted
```

The correct merged order is:

```text
a, b, c, d, e
```

The next key can come from any level:

```text
step 1: level[1] supplies a
step 2: level[0] supplies b
step 3: level[1] supplies c
step 4: level[0] supplies d
step 5: level[1] supplies e
```

Therefore, a range iterator must observe all current level cursors and select
the next winner globally.

---

## 5. Merge Sort Without Arrays

Merge sort is often taught using arrays, but merging requires only ordered
iteration.

For two sorted inputs:

```text
input A: b, d
input B: a, c, e
```

Maintain one cursor in each:

```text
input A: [b] d
          ▲

input B: [a] c e
          ▲
```

Choose the smaller current key, emit it, and advance the source or sources
that have been consumed:

```text
compare b and a → emit a
compare b and c → emit b
compare d and c → emit c
compare d and e → emit d
only e remains  → emit e
```

Output:

```text
a, b, c, d, e
```

Nothing required random access to the complete sources. Each source only
needed:

```text
Valid()
Key()
Val()
Next()
Prev()
```

This is why the algorithm works with both in-memory arrays and on-disk
SSTables.

---

## 6. The Main Mock Example

We will use two levels throughout this README.

```text
level[0] — newer, higher priority
├── b → newB
└── d → newD

level[1] — older, lower priority
├── a → oldA
├── b → oldB
├── c → oldC
└── e → oldE
```

Linear representation:

```text
level[0]: [b:newB, d:newD]
level[1]: [a:oldA, b:oldB, c:oldC, e:oldE]
```

Expected merged result:

```text
a → oldA
b → newB     duplicate resolved in favor of level[0]
c → oldC
d → newD
e → oldE
```

Tree visualization:

```text
Merged view
│
├── a → oldA  ◄── level[1]
├── b → newB  ◄── level[0], wins over b→oldB
├── c → oldC  ◄── level[1]
├── d → newD  ◄── level[0]
└── e → oldE  ◄── level[1]
```

---

## 7. Creating the Merged Iterator

Calling `MergedSortedKV.Iter()` does not merge all records immediately. It
creates one sub-iterator per level:

```text
MergedSortedKV.Iter()
│
├── level[0].Iter() ──► sub-iterator 0
├── level[1].Iter() ──► sub-iterator 1
├── ...
│
├── select the current smallest key
└── return MergedSortedKVIter
```

The merged iterator stores:

```go
type MergedSortedKVIter struct {
    levels []SortedKVIter
    which  int
}
```

Meaning:

```text
levels
└── current cursor for every underlying sorted source

which
└── index of the source currently supplying Key() and Val()
```

Initial state for the mock example:

```text
levels[0] → b:newB
levels[1] → a:oldA

smallest key = a
which = 1
```

The merged iterator itself does not copy `a` or `oldA`. It remembers that
level 1 is the winner:

```text
MergedSortedKVIter
├── levels[0] → b:newB
├── levels[1] → a:oldA
└── which = 1
```

---

## 8. What `which` Means

`which` is not a record position. It is a level index.

```text
which = 0 → current output comes from levels[0]
which = 1 → current output comes from levels[1]
which = 2 → current output comes from levels[2]
```

For the initial state:

```text
levels[0].Key() = b
levels[1].Key() = a
which = 1
```

Therefore:

```text
Merged Key() → levels[1].Key() → a
Merged Val() → levels[1].Val() → oldA
```

The accessors delegate to the winning iterator:

```text
MergedSortedKVIter.Key()
        │
        ▼
levels[which].Key()
```

`which = -1` is the sentinel for no valid winner:

```text
which >= 0 → merged iterator is valid
which = -1 → every level is exhausted in the current direction
```

---

## 9. Selecting the Lowest Key

`levelsLowest` examines the current key from each valid level and returns the
index with the smallest key.

Conceptual algorithm:

```text
winner = none

for each level from 0 upward:
    if level is invalid:
        skip it
    if there is no winner yet:
        choose this level
    else if this level's key is strictly smaller:
        replace the winner

return winner
```

### Initial selection in the mock example

```text
level[0] current key = b
level[1] current key = a
```

Trace:

```text
start: winner = -1

inspect level[0]: valid b
→ no winner yet
→ winner = 0, winnerKey = b

inspect level[1]: valid a
→ a < b
→ winner = 1, winnerKey = a

return 1
```

Result:

```text
which = 1
current merged key = a
```

### Tie handling creates priority

Later both levels point to `b`:

```text
level[0] = b:newB
level[1] = b:oldB
```

Trace:

```text
inspect level[0]: choose b:newB as first winner
inspect level[1]: key equals current winner's b
                  not strictly smaller
                  do not replace winner
```

Result:

```text
which = 0
b → newB wins
```

The strict comparison is important:

```text
replace winner only if candidate < winner
not if candidate <= winner
```

Because levels are scanned from index `0`, equal keys retain the highest
priority source.

---

## 10. Forward Merge: Complete Trace

Initial cursors:

```text
level[0]: [b:newB]  d:newD
           ▲

level[1]: [a:oldA]  b:oldB  c:oldC  e:oldE
           ▲

which = 1 → output a:oldA
```

### Output 1: `a → oldA`

When the caller asks for `Next()`, the merge must move beyond key `a`.

```text
current merged key = a
```

Examine each level:

```text
level[0] key b
a >= b? false
→ leave level[0] at b

level[1] key a
a >= a? true
→ advance level[1] from a to b
```

New cursors:

```text
level[0]: [b:newB] d:newD
level[1]: [b:oldB] c:oldC e:oldE
```

Both keys are `b`. Priority chooses level 0:

```text
which = 0
output b → newB
```

### Output 2: `b → newB`

Current key:

```text
cur = b
```

Examine each level:

```text
level[0] key b
b >= b? true
→ advance to d

level[1] key b
b >= b? true
→ advance to c
```

This is how the duplicate disappears: every iterator currently positioned at
the already-emitted key `b` moves past it.

New cursors:

```text
level[0]: b:newB [d:newD]
level[1]: a:oldA b:oldB [c:oldC] e:oldE
```

Lowest key is `c`:

```text
which = 1
output c → oldC
```

### Output 3: `c → oldC`

```text
cur = c
```

```text
level[0] key d
c >= d? false
→ stay at d

level[1] key c
c >= c? true
→ advance to e
```

New cursors:

```text
level[0]: [d:newD]
level[1]: [e:oldE]
```

Lowest key is `d`:

```text
which = 0
output d → newD
```

### Output 4: `d → newD`

```text
cur = d
```

```text
level[0] key d
d >= d? true
→ advance past end, level[0] invalid

level[1] key e
d >= e? false
→ stay at e
```

Only level 1 remains valid:

```text
which = 1
output e → oldE
```

### Output 5: `e → oldE`

```text
cur = e
```

```text
level[0] invalid
→ Next() leaves its end position invalid

level[1] key e
e >= e? true
→ advance past end
```

All levels are invalid:

```text
levelsLowest(...) = -1
which = -1
Merged Valid() = false
```

Complete result:

```text
a:oldA → b:newB → c:oldC → d:newD → e:oldE → end
```

---

## 11. Understanding the `Next()` Condition

The central forward condition is conceptually:

```text
advance a sub-iterator when:

sub is invalid
OR
sub.Key() <= current merged key
```

The comparison may be written as:

```go
bytes.Compare(cur, sub.Key()) >= 0
```

Because:

```text
Compare(cur, subKey) > 0 → cur > subKey
Compare(cur, subKey) = 0 → cur = subKey
Compare(cur, subKey) < 0 → cur < subKey
```

So `>= 0` means:

```text
cur >= subKey
equivalently
subKey <= cur
```

Why advance everything at or behind the current key?

```text
subKey < cur
└── that source is behind the merged cursor and must catch up

subKey = cur
└── that source contains the emitted key, possibly as a duplicate

subKey > cur
└── it is already a future candidate; leave it in place
```

After advancing the necessary sub-iterators, `levelsLowest` selects the next
globally smallest key.

---

## 12. Deduplication Is More Than Choosing a Winner

Priority selection determines which duplicate value is visible:

```text
level[0]: b → newB   winner
level[1]: b → oldB   loser
```

But choosing the winner alone is insufficient. If only level 0 advanced, the
next selection would expose level 1's stale duplicate:

```text
output b:newB
advance only level[0]

level[0] → d
level[1] → b:oldB

next output would be b:oldB     wrong
```

Therefore, after emitting `b`, `Next()` advances every level whose current key
is `b`:

```text
before:
level[0] → b:newB
level[1] → b:oldB

after:
level[0] → d:newD
level[1] → c:oldC
```

The two parts work together:

```text
strict tie selection
└── chooses the newest value

advance every equal key
└── prevents older duplicates from appearing afterward
```

This same behavior is required when physically merging SSTables. If stale
duplicates were retained forever, storage would continue growing like an
uncompacted log.

---

## 13. Backward Merge

Backward iteration is the mirror image of forward iteration.

Forward:

```text
advance sources at or below current key
select lowest remaining key
```

Backward:

```text
move backward sources at or above current key
select highest remaining key
```

The central backward condition is:

```text
sub is invalid
OR
sub.Key() >= current merged key
```

It may be written as:

```go
bytes.Compare(cur, sub.Key()) <= 0
```

Because `cur <= subKey` means the sub-iterator is at or ahead of the current
key and must move backward.

After movement, `levelsHighest` chooses the greatest current key.

---

## 14. Selecting the Highest Key

`levelsHighest` mirrors `levelsLowest`:

```text
winner = none

for each valid level from 0 upward:
    if there is no winner:
        choose it
    else if candidate key is strictly greater:
        replace winner

return winner
```

It also preserves upper-level priority on equal keys:

```text
level[0] → b:newB
level[1] → b:oldB

scan level[0] first → winner = level[0]
level[1] is equal, not strictly greater
→ level[0] remains winner
```

Strict comparisons preserve priority in both directions:

```text
levelsLowest:  replace only when candidate < winner
levelsHighest: replace only when candidate > winner
```

---

## 15. Reverse from the End

After completing forward iteration, each sub-iterator is positioned just
after its own final record:

```text
level[0] position = len(level[0])
level[1] position = len(level[1])
which = -1
```

Calling merged `Prev()` must restore the final record of each level:

```text
level[0].Prev() → d:newD
level[1].Prev() → e:oldE
```

Then `levelsHighest` chooses `e`:

```text
e:oldE → d:newD → c:oldC → b:newB → a:oldA
```

Reverse result:

```text
e:oldE
d:newD
c:oldC
b:newB     level[0] still wins the duplicate
a:oldA
```

At the beginning, another `Prev()` moves each iterator to position `-1` and
sets:

```text
which = -1
Valid() = false
```

---

## 16. Changing Direction Midway

Supporting direction changes is more difficult than supporting one-way merge.
The sub-iterators are not necessarily all positioned on the merged current
key. Some may already point to a future candidate.

Suppose forward iteration has reached `c`:

```text
merged current output = c:oldC

underlying cursors:
level[0] → d:newD
level[1] → c:oldC
which = 1
```

Now call `Prev()`.

### Move every source at or above `c` backward

```text
level[0] key d
c <= d → Prev()
d moves back to b:newB

level[1] key c
c <= c → Prev()
c moves back to b:oldB
```

New cursors:

```text
level[0] → b:newB
level[1] → b:oldB
```

`levelsHighest` sees a tie and preserves level 0 priority:

```text
merged result = b:newB
```

Now immediately call `Next()`.

```text
current merged key = b

level[0] b <= b → advance to d
level[1] b <= b → advance to c

levelsLowest(d, c) → c
```

The merged iterator returns to:

```text
c:oldC
```

Direction trace:

```text
... → b → c
         │
         │ Prev()
         ▼
         b
         │
         │ Next()
         ▼
         c → d → ...
```

The `<= current` and `>= current` movement rules are what make this work. A
method that moved only the winning iterator would fail during duplicates or
direction changes.

---

## 17. Invalid Sub-Iterators and Direction Recovery

An individual iterator can be invalid in two different ways:

```text
position = -1          before the beginning
position = len(keys)   after the end
```

These positions behave differently when direction changes:

```text
from position -1:
Next() → position 0, first record

from position len:
Prev() → position len-1, last record
```

This explains why `Next()` and `Prev()` also call the corresponding operation
on invalid sub-iterators:

```text
merged iterator is before beginning
└── Next() asks every invalid source to move forward
    └── sources return to their first records

merged iterator is after end
└── Prev() asks every invalid source to move backward
    └── sources return to their last records
```

Calling the same direction again at the exhausted boundary remains invalid:

```text
after end + Next()   → still after end
before start + Prev() → still before start
```

---

## 18. Empty Levels

The merge must work when any or all levels are empty.

### Both empty

```text
level[0]: []
level[1]: []

levelsLowest → -1
merged Valid() → false
```

### First nonempty, second empty

```text
level[0]: [x, z]
level[1]: []

merged: x, z
```

### First empty, second nonempty

```text
level[0]: []
level[1]: [x, z]

merged: x, z
```

The selection helpers skip invalid iterators, so empty sources need no special
case outside the ordinary algorithm.

---

## 19. K-Way Merge

Although MemTable plus SSTable gives two sources, the implementation accepts
any number `k`:

```text
level[0]: b, h
level[1]: a, d, j
level[2]: c, e, i
level[3]: f, g
```

At each forward step:

```text
observe current key from every valid level
                 │
                 ▼
choose global minimum, with earlier-level tie priority
                 │
                 ▼
emit one logical key
                 │
                 ▼
advance every source at or behind emitted key
```

The merged output remains:

```text
a, b, c, d, e, f, g, h, i, j
```

The code complexity is not fundamentally different for two or many sources
because the level iterators are stored in a slice and scanned uniformly.

---

## 20. Lazy View, Not Materialized Output

`MergedSortedKV.Iter()` creates a live merged view. It does not immediately
construct:

```text
mergedKeys = [a, b, c, d, e]
mergedVals = [...]
```

Instead:

```text
RAM state for merge
├── one iterator per level
├── one integer `which`
└── current payloads owned by underlying iterators
```

Benefits:

```text
does not duplicate the complete dataset in memory
can merge SSTables larger than RAM
can begin returning results before reaching the end
can feed another streaming consumer, such as an SSTable builder
```

This is exactly why the abstraction can later serve both:

```text
range query
└── application consumes merged iterator

compaction
└── SSTable builder consumes merged iterator
```

---

## 21. Deletions Require Tombstones

This chapter deliberately does not finish deletion semantics, but it explains
why simply removing a MemTable key is unsafe once an older SSTable exists.

Suppose:

```text
older SSTable
└── b → oldB

newer MemTable before deletion
└── b → newB
```

If deletion physically removes `b` from the MemTable:

```text
newer MemTable
└── no b

older SSTable
└── b → oldB
```

The merged lookup would expose the old value again:

```text
GET b → oldB       incorrect resurrection
```

The newer level must instead remember the deletion:

```text
newer MemTable
└── b → TOMBSTONE

older SSTable
└── b → oldB
```

Priority then gives:

```text
b → TOMBSTONE wins
→ logical result: key is deleted
```

Eventually compaction may discard both the tombstone and every older version
when it is safe. Chapter 0604's merge iterator resolves duplicate priority,
but the deletion marker plumbing is deferred to the next step.

Chapter boundary:

```text
0604 now
├── ordered k-way merge
├── duplicate priority
└── deduplicated iterator output

0605 next
├── deletion marker in iterator interface
├── MemTable + SSTable integration
├── compaction output
└── safe log/SSTable lifecycle
```

---

## 22. Range Merge and Physical Compaction Share an Algorithm

The same merge behavior has two consumers.

### Query-time merged view

```text
MemTable ──────┐
               ├── lazy merge ──► application range results
SSTable ───────┘
```

No new file is created. The iterator chooses records on demand.

### Compaction-time materialization

```text
newer sorted source ──┐
                      ├── merged iterator ──► new SSTable builder
older SSTable ────────┘
```

Now the same logical stream is serialized into a replacement SSTable:

```text
old sources
    │
    ▼
priority-aware deduplicated merge
    │
    ▼
new compacted SSTable
```

This reuse is one reason the chapter builds the merge around interfaces rather
than concrete arrays.

---

## 23. Bloom Filters and Point-Lookup Optimization

A single-key lookup can be viewed as a tiny range query, so a merged seek can
provide correct behavior. But it may initialize or inspect every level even
when the desired key is in a recent upper level.

### Newest-first direct lookup

For hot, recently updated keys:

```text
GET hot-key
│
├── level[0] → found
└── stop
```

This can be much cheaper than setting up a merged iterator across all levels.

### Bloom filter role

Each immutable SSTable can carry a compact Bloom filter:

```text
BloomFilter.MayContain(key)
├── false → key definitely absent; skip SSTable
└── true  → key may exist; perform real lookup
```

The critical guarantee is:

```text
false positives are allowed
└── filter says “maybe” but key is absent

false negatives are not allowed
└── filter must never say “absent” for an existing key
```

Why Bloom filters fit immutable SSTables:

```text
SSTable keys do not change after construction
└── build filter once from all keys
    └── no in-place deletion support is required
```

The filter is much smaller than the SSTable and is likely to remain cached in
memory. This discussion is architectural context; 0604 does not implement a
Bloom filter.

---

## 24. Complexity

Let:

```text
K = number of sorted levels
M = total number of input records across all levels
U = number of unique output keys
```

### Iterator initialization

```text
create K sub-iterators: O(K)
select first winner:    O(K)
memory for cursors:     O(K)
```

### Each emitted key

The chapter's straightforward implementation scans all `K` levels to advance
eligible cursors and scans them again to choose the next winner:

```text
per unique output: O(K)
```

Overall selection work is approximately:

```text
O(U × K)
```

Every input iterator also advances through its records, so underlying record
movement totals `O(M)`.

The merged view itself stores only `K` iterator references plus current
records:

```text
merge bookkeeping memory: O(K)
```

For very large `K`, a priority queue can reduce winner selection toward
`O(log K)` per output. The chapter intentionally uses the simpler linear scan,
which is effective when the number of levels is modest and makes priority,
deduplication, and direction reversal explicit.

### I/O behavior

If a level is an SSTable, advancing its iterator can perform disk reads. Merge
logic does not erase those I/O costs; it coordinates them in sorted order and
propagates any error to the caller.

---

## 25. Error Propagation

Each underlying iterator can return an error from `Next()` or `Prev()`:

```text
Merged Next()
└── sub.Next()
    └── disk read fails
        └── return error immediately
```

The merged iterator must not ignore the failure and choose a winner from
partially advanced state.

Similarly, initialization can fail:

```text
MergedSortedKV.Iter()
├── level[0].Iter() succeeds
├── level[1].Iter() fails
└── return error
```

The error-aware interface allows the same merge to operate over:

```text
SortedArray iterator
└── movement normally cannot fail

SortedFile iterator
└── movement may perform failing I/O
```

---

## 26. What the Chapter Test Covers

The test constructs values based on level priority:

```text
level[0] values use A
level[1] values use B
level[2] values would use C
```

That makes duplicate winner selection visible.

### Empty inputs

```text
[] + [] → []
```

### One nonempty input

```text
[x, z] + [] → [x, z]
[] + [x, z] → [x, z]
```

### Complete duplication

```text
level[0]: x→A, z→A
level[1]: x→B, z→B

merged:   x→A, z→A
```

This verifies both deduplication and upper-level priority.

### Interleaving inputs

```text
level[0]: x→A, z→A
level[1]: w→B, y→B

merged:   w→B, x→A, y→B, z→A
```

Tree view:

```text
merged
├── w → B  from level[1]
├── x → A  from level[0]
├── y → B  from level[1]
└── z → A  from level[0]
```

### Reverse iteration

After reaching the end, the test repeatedly calls `Prev()` and expects:

```text
z → y → x → w
```

### Direction changes

The test also moves forward, backward, and forward again. This guards against
an implementation that works only when direction never changes.

The expected list is built with stable sorting. Stability preserves the first
occurrence of an equal key, matching the rule that earlier levels have higher
priority.

---

## 27. Implementation Order

A clean implementation sequence is:

```text
1. Define MergedSortedKV as []SortedKV
   │
2. Implement Iter()
   ├── create one iterator per source
   └── select initial lowest key
   │
3. Define MergedSortedKVIter
   ├── levels
   └── which
   │
4. Implement Valid(), Key(), and Val()
   │
5. Implement levelsLowest()
   │
6. Implement Next()
   ├── remember current key
   ├── advance all sources at or behind it
   └── select next lowest key
   │
7. Implement levelsHighest()
   │
8. Implement Prev()
   ├── remember current key
   ├── move backward all sources at or ahead of it
   └── select next highest key
   │
9. Run forward, reverse, duplicate, empty, and direction-change tests
```

Dependency graph:

```text
levelsLowest ─────► Iter initialization
       │
       └──────────► Next

levelsHighest ────► Prev

underlying iterator methods
├── Valid
├── Key
├── Val
├── Next
└── Prev
       │
       ▼
MergedSortedKVIter
```

---

## 28. Common Misunderstandings

### “Merge means concatenate level 0 and level 1.”

No. Concatenation does not preserve global key order when key ranges overlap
or interleave.

### “The merge first copies every record into one large array.”

No. It is lazy and retains one iterator per source.

### “`which` is the position of the current key.”

No. `which` identifies the level iterator supplying the current key.

### “The first level should always supply the next result.”

No. Level order decides priority only for equal keys. For different keys, the
globally smallest or largest key decides.

### “Choosing level 0 for an equal key automatically removes duplicates.”

No. Every underlying iterator positioned at the emitted duplicate key must
also advance past it.

### “Only the winning iterator needs to move.”

No. That fails with duplicate keys and can fail when changing direction.

### “`Next()` always calls `Next()` on every level.”

No. It advances levels at or behind the current key and leaves future keys in
place.

### “`levelsLowest` may use `<=` when replacing the winner.”

That would let a later, lower-priority level replace an earlier level on an
equal key. Strict `<` preserves priority.

### “Backward iteration can just reverse the final merged array.”

There is no materialized final array. `Prev()` coordinates the same live
sub-iterators in the opposite direction.

### “Deleting a key from the MemTable is enough.”

Not after older SSTables exist. A tombstone is needed to prevent an older
value from reappearing. Tombstone integration is deferred to 0605.

### “A Bloom filter proves that a key exists.”

No. It can prove absence or report possible presence. A possible match still
requires a real lookup.

### “0604 already adds SSTables to the main `KV` engine.”

No. It implements and tests the reusable merge iterator. Main-engine
integration comes next.

---

## 29. Review Questions

### 1. Why can a key exist in both MemTable and SSTable?

New updates are written to the MemTable without immediately modifying the
immutable older SSTable.

### 2. Which duplicate wins?

The earliest level in `MergedSortedKV`, representing the upper and newer
source.

### 3. Why can point lookup search one level at a time?

The first exact match from newest to oldest is authoritative.

### 4. Why can a range query not finish one level before reading another?

The next globally smallest key may come from any level.

### 5. What state does the merged iterator keep?

One underlying iterator per level and `which`, the index of the currently
winning level.

### 6. What does `which = -1` mean?

No underlying iterator has a valid current key, so the merged iterator is
invalid.

### 7. How does `levelsLowest` preserve priority?

It scans levels in priority order and replaces the winner only for a strictly
smaller key, not an equal key.

### 8. Why does `Next()` advance every iterator at the current key?

To ensure lower-priority duplicate versions cannot appear as later output.

### 9. Why might `Next()` advance an iterator whose key is below the current
key?

That iterator is behind the merged cursor, which can happen after changing
direction, so it must catch up.

### 10. How does backward merge differ from forward merge?

It moves sources at or above the current key backward and selects the greatest
remaining key.

### 11. Why do invalid sub-iterators still receive movement calls?

An iterator invalid before the beginning can be restored by `Next()`, and one
invalid after the end can be restored by `Prev()`.

### 12. Why are strict comparisons used by both winner helpers?

Strict comparisons keep the first, highest-priority level as winner when keys
are equal.

### 13. What is the merged output for `[x,z]` above `[w,y]`?

```text
w, x, y, z
```

### 14. What value wins when both levels contain `x`?

The value from the first, upper level.

### 15. Why is the merge described as lazy?

It produces one result at a time from live iterators rather than constructing
the entire merged dataset first.

### 16. How can the same iterator support compaction?

The SSTable builder can consume the deduplicated sorted stream and serialize
it into a replacement file.

### 17. Why is physically deleting from the MemTable unsafe?

An older SSTable version could become visible again. A higher-priority
tombstone must hide it.

### 18. What is the chapter implementation's per-output selection cost?

`O(K)` for `K` levels because it scans the level iterators.

### 19. What can a Bloom filter safely say?

That a key is definitely absent or that it may be present.

### 20. Why are Bloom filters a natural fit for SSTables?

SSTables are immutable, so the filter can be built once and does not need to
support deleting individual keys.

---

## 30. Complete Mental Model

```text
                  MergedSortedKV
                         │
                         │ Iter()
                         ▼
             create one iterator per level
                         │
           ┌─────────────┼─────────────┐
           ▼             ▼             ▼
       level[0]       level[1]      level[2]
       newest                         oldest
           │             │             │
           └─────────────┼─────────────┘
                         ▼
                select winning key
                ├── forward: lowest
                └── backward: highest
                         │
                         ▼
               equal key in many levels?
                ├── earliest level wins
                └── all duplicate cursors move past it
                         │
                         ▼
                 one sorted logical view
```

The larger LSM relationship is:

```text
fast foreground update
       │
       ▼
new state enters upper level
       │
       ├── queries merge upper and lower views
       │
       └── compaction later materializes the same merge
                   │
                   ▼
        stale duplicates are reclaimed
```

---

## Crucial Takeaways

- The database needs a merged view because newer MemTable records and older
  SSTable records coexist.
- Level order encodes recency and priority: earlier levels win duplicate keys.
- Point lookup can search levels newest-first, but sorted range iteration must
  coordinate all levels because the next key may come from any source.
- Merge sort requires ordered iterators, not necessarily arrays.
- `MergedSortedKVIter` stores one iterator per level and `which`, the currently
  winning level index.
- Forward iteration advances all sources at or behind the current key and then
  selects the smallest remaining key.
- Backward iteration moves all sources at or ahead of the current key backward
  and then selects the largest remaining key.
- Strict winner comparisons preserve upper-level priority when keys are equal.
- Advancing every duplicate source is necessary to prevent stale versions
  from appearing later.
- The movement rules also make midstream direction changes correct.
- The merge is lazy and uses `O(K)` cursor state rather than materializing the
  complete output in memory.
- The same merged iterator can power range queries and later feed an SSTable
  builder during compaction.
- Tombstones are required to prevent deleted keys from reappearing from older
  levels, but their integration is deferred to 0605.
- Bloom filters can skip SSTables that definitely lack a point-query key, but
  they are architectural context rather than a 0604 implementation task.
