# Chapter 0901: Snapshot Isolation

## Goal of this chapter

Chapter 0805 made each transaction **atomic**: all of its writes commit together or
none of them do. Chapter 0901 addresses a different problem: while a transaction is
running, another transaction may commit and change what the first transaction sees.

The goal is to give each transaction a stable read view:

```text
At transaction start: capture the current committed state.
During the transaction: keep reading that same state plus your own writes.
After another transaction commits: do not change the first transaction's view.
```

The shortest progression is:

```text
0805: Which writes commit together?                    Atomicity
0901: Which committed version may this transaction see? Snapshot isolation
0902: Did another transaction invalidate my write?      Conflict detection
0903: Can these operations run safely on many threads?  Synchronization
```

The central 0901 guarantee is:

```text
tx1 starts when k = old
tx2 changes k = new and commits
tx1 still reads k = old
new transactions read k = new
```

This is isolation between **concurrent transactions**. They do not need to run on
different OS threads to be concurrent. Their lifetimes only need to overlap.

```text
tx1 begins ─────────────────────────────── abort/commit
       tx2 begins ───── commit

The lifetimes overlap, so the transactions are concurrent.
```

## Reading map

Page numbers below use the books' printed pagination. A PDF viewer may include
front matter and therefore display a different page number.

### *Build Your Own Database From Scratch in Go*

- Chapter 0901, **“Snapshot Isolation,” pages 93–94**
- Page 93: isolation, concurrent transactions, serial execution, locks, deadlocks,
  and MVCC
- Page 94: immutable SSTables as snapshots, the mutable MemTable problem, and
  five possible snapshot strategies

The chapter chooses a deliberately small copy-on-write design:

1. Copy the MemTable's `SortedArray` value when a transaction begins.
2. Never mutate the current MemTable in place during commit.
3. Build a new merged MemTable and swap it into `kv.mem`.

### Martin Kleppmann, *Designing Data-Intensive Applications*

The relevant material is Chapter 7, **“Transactions”**:

- **Pages 224–225, “Weak Isolation Levels”**: why concurrency anomalies are hard
  to reproduce and what serializable execution means
- **Pages 225–228, “Read Committed”**: dirty reads, dirty writes, and the limits
  of seeing only committed data
- **Pages 228–233, “Snapshot Isolation and Repeatable Read”**: read skew,
  consistent snapshots, MVCC, visibility rules, indexes, and copy-on-write trees
- **Pages 233–237, “Preventing Lost Updates”**: why stable reads alone do not make
  read-modify-write safe
- **Pages 237–242, “Preventing Write Skew and Phantoms”**: anomalies that ordinary
  snapshot isolation still allows
- **Pages 242–257, “Serializability”**: serial execution, two-phase locking, and
  serializable snapshot isolation (SSI)

For 0901 itself, pages **224–233** are the essential reading. Pages **233–257**
explain the boundary of this chapter and prevent us from overclaiming what our
implementation guarantees.

Helpful external references:

- [Isolation levels and read phenomena](https://en.wikipedia.org/wiki/Isolation_%28database_systems%29)
- [Snapshot isolation and write skew](https://en.wikipedia.org/wiki/Snapshot_isolation)
- [MVCC](https://en.wikipedia.org/wiki/Multiversion_concurrency_control)
- [PostgreSQL transaction isolation](https://www.postgresql.org/docs/17/transaction-iso.html)

## Atomicity and isolation solve different problems

It is easy to mix up chapters 0805 and 0901 because both use `KVTX`.

### Atomicity asks about writes

```text
Transaction writes A, B, and C.

Allowed outcomes:
    A, B, C all commit
    none commit

Forbidden outcome:
    A and B commit, but C does not
```

0805 solves this with private `KVTX.updates`, WAL records, `EntryCommit`, `fsync`,
and recovery through the last commit marker.

### Isolation asks about reads during overlap

```text
tx1 starts and reads A.
tx2 changes A and commits.
tx1 reads A again.

Question:
    Does tx1 see its original A or tx2's newer A?
```

0901 makes `tx1` continue seeing the version that existed when `tx1` began.

```text
Atomicity:  Do my writes become visible as one unit?
Isolation:  Which version of other transactions' writes may I observe?
Durability: Will a successful commit survive a crash?
```

## The bug inherited from 0805

In 0805, a transaction's read levels include a pointer to the live MemTable:

```go
func (kv *KV) NewTX() *KVTX {
    tx := &KVTX{target: kv}
    tx.levels = MergedSortedKV{&tx.updates, &kv.mem}
    // ...SSTables...
    return tx
}
```

The key expression is:

```go
&kv.mem
```

Every transaction points to the same mutable `kv.mem` object.

Consider this state:

```text
kv.mem:
    k1 = v1
    k2 = v2
```

Create two transactions:

```go
tx1 := kv.NewTX()
tx2 := kv.NewTX()
```

Their views are effectively:

```text
tx1.levels ─┐
            ├──► the same live kv.mem
tx2.levels ─┘
```

Now `tx2` stages and commits:

```text
delete k1
set k2 = xxx
```

The 0805 `updateMem` mutates the shared MemTable:

```go
kv.mem.Del(...)
kv.mem.Set(...)
```

After `tx2.Commit()`:

```text
kv.mem:
    k1 = deleted
    k2 = xxx
```

Because `tx1.levels` points to the live `kv.mem`, `tx1` suddenly sees those
changes—even though `tx1` started before `tx2` committed.

```text
tx1 first read:   k1=v1, k2=v2
tx2 commits:      delete k1, set k2=xxx
tx1 second read:  k1 missing, k2=xxx   ← unstable view
```

This is a non-repeatable read. If `tx1` scans several keys and sees some before the
commit and others afterward, it may also observe read skew: a combination of values
that never existed together as one committed database state.

## The desired behavior

After 0901:

```text
Initial committed state:
    k1 = v1
    k2 = v2

tx1 starts:
    snapshot = {k1=v1, k2=v2}

tx2 starts:
    snapshot = {k1=v1, k2=v2}
    own updates = {delete k1, k2=xxx}

tx2 reads:
    k1 missing
    k2 = xxx

tx2 commits:
    current database = {k1 missing, k2=xxx}

tx1 reads again:
    k1 = v1
    k2 = v2

tx3 starts after tx2 commits:
    k1 missing
    k2 = xxx
```

The rule is:

```text
A transaction sees:

1. Its own private updates, then
2. The committed snapshot captured at its start.
```

It does not see commits that happened after it started.

## Snapshot does not mean copying the whole database

A logical snapshot is a stable point-in-time view. It does not necessarily mean
duplicating every byte.

This engine already has a useful property:

```text
SSTables are immutable.
```

Once an SSTable is created, ordinary writes do not edit its contents. Newer values
appear in newer layers. A transaction can therefore keep references to the SSTables
that formed its starting state.

The MemTable is different:

```text
SSTable: immutable after creation
MemTable: changed by each commit
```

Thus, 0901 only needs to fix how transactions observe the MemTable.

## Copy-on-write: the general concept

[Copy-on-write (COW)](https://en.wikipedia.org/wiki/Copy-on-write) is a resource-management
technique for sharing data efficiently. Readers initially share the same data instead
of receiving complete private copies. When someone needs to write, the shared version
is left unchanged and a new version is created for the writer.

The rule is:

```text
Share while reading.
Copy when writing.
Never overwrite data that an existing reader still needs.
```

Suppose two readers share version `A`:

```text
reader 1 ──┐
           ├──► version A: [k1=v1, k2=v2]
reader 2 ──┘
```

If reader 2 wants to change `k2`, directly modifying version `A` would also change
what reader 1 sees:

```text
Incorrect in-place write:

reader 1 ──┐
           ├──► version A: [k1=v1, k2=xxx]
reader 2 ──┘

reader 1 unexpectedly sees the change.
```

With copy-on-write, the writer creates version `B` from `A`, applies the change to
`B`, and then switches its current reference to `B`:

```text
Before the write:

reader 1 ──┐
           ├──► version A: [k1=v1, k2=v2]
writer   ──┘

After the write:

reader 1 ─────► version A: [k1=v1, k2=v2]
writer   ─────► version B: [k1=v1, k2=xxx]
```

The original data is copied only because a write is occurring. A reader that never
writes does not require an eager deep copy. This makes snapshots cheap to create,
although writes require extra allocation and old versions consume memory while they
are still referenced. Once no transaction references an old version, Go's garbage
collector can reclaim it.

### Shallow copy and copy-on-write are different parts of the design

`SortedArray` contains three Go slices:

```go
type SortedArray struct {
    keys    [][]byte
    vals    [][]byte
    deleted []bool
}
```

The statement `mem := kv.mem` copies the `SortedArray` struct and its slice headers,
but it does not duplicate the slices' backing arrays:

```text
kv.mem box ──► backing arrays A
tx1 mem box ─► backing arrays A
```

This is a **shallow copy**: there are two independent boxes, but both initially refer
to the same data. It is safe only if backing arrays `A` are treated as immutable from
that point onward.

Copy-on-write provides that missing rule. A later commit does not call `Set` or `Del`
on arrays `A`. It constructs new arrays `B` and changes only the live `kv.mem` box:

```text
Before commit:

tx1 mem box ─┐
             ├──► backing arrays A
kv.mem box ──┘

After commit:

tx1 mem box ─────► backing arrays A (unchanged snapshot)
kv.mem box ──────► backing arrays B (new current state)
```

Thus, shallow copying makes transaction creation cheap, while copy-on-write prevents
future commits from changing the shared old data.

## How 0901 applies copy-on-write

This engine applies copy-on-write at the **whole MemTable level**. It is a deliberately
simple, coarse-grained form of COW: every successful top-level commit rebuilds the
MemTable rather than copying only the modified records or tree paths.

Assume the current MemTable is `M0`:

```text
M0:
    k1 = v1
    k2 = v2
```

When `tx1` starts, `KV.NewTX` gives it a separate `mem` box that points to `M0`:

```text
tx1.snapshot ──► M0
kv.mem ─────────► M0
```

When `tx2` stages changes, they remain in `tx2.updates`:

```text
tx2.updates:
    k1 = deleted
    k2 = xxx

M0 remains unchanged.
```

On `tx2.Commit()`, the engine performs these operations in order:

1. `updateLog` writes every staged operation and a commit record to the WAL.
2. `updateMem` merges `tx2.updates` over the current `M0`.
3. The merge is pushed into a fresh `SortedArray`, producing `M1`.
4. `kv.mem = merged` publishes `M1` as the current MemTable.

```text
tx2.updates ─┐
             ├── merge into fresh storage ──► M1
M0 ──────────┘

M1:
    k1 = deleted
    k2 = xxx
```

After publication:

```text
tx1.snapshot ──► M0: [k1=v1,      k2=v2]
kv.mem ─────────► M1: [k1=deleted, k2=xxx]
```

Therefore:

```text
tx1.Get(k1)  → v1       transaction keeps its starting snapshot
tx1.Get(k2)  → v2
kv.Get(k1)   → missing  new transaction reads the current MemTable
kv.Get(k2)   → xxx
```

The responsibilities are divided between two functions:

```text
KV.NewTX:
    retain a reference to the current version M0

KV.updateMem:
    construct M1 without mutating M0, then publish M1
```

Both are necessary. Capturing `M0` without copy-on-write would let in-place mutations
leak into the snapshot. Copy-on-write without capturing `M0` would make an existing
transaction follow the live `kv.mem` field to `M1`.

## The two implementation changes

Only two behavioral changes are required in `kv.go`.

### Change 1: capture the MemTable at transaction start

The 0805 code uses the live field:

```go
tx.levels = MergedSortedKV{&tx.updates, &kv.mem}
```

The 0901 version first copies the `SortedArray` value:

```go
func (kv *KV) NewTX() *KVTX {
    tx := &KVTX{target: kv}

    mem := kv.mem // snapshot the slice headers
    tx.levels = MergedSortedKV{&tx.updates, &mem}

    for i := range kv.main {
        tx.levels = append(tx.levels, &kv.main[i])
    }
    return tx
}
```

The transaction now points to its local `mem`, not to the live `kv.mem` field:

```text
Before:

tx1.levels ─┐
            ├──► kv.mem
tx2.levels ─┘

After:

tx1.levels ───► mem snapshot M0
tx2.levels ───► mem snapshot M0
kv.mem      ──► current MemTable M0
```

Go keeps the local `mem` alive after `NewTX` returns because the transaction stores
a pointer to it.

### Change 2: replace the MemTable instead of mutating it

Copying `SortedArray` alone is insufficient. `SortedArray` contains slices:

```go
type SortedArray struct {
    keys    [][]byte
    vals    [][]byte
    deleted []bool
}
```

A Go struct copy copies the slice headers, not all backing arrays:

```text
mem snapshot.keys ──► backing array A
kv.mem.keys       ──► backing array A
```

If commit continued calling `kv.mem.Set` and `kv.mem.Del`, those methods could
modify shared backing arrays and corrupt the supposedly frozen snapshot.

Therefore, 0901 changes `updateMem` to build a new `SortedArray`:

```go
func (kv *KV) updateMem(tx *KVTX) {
    merged := SortedArray{}
    iter, err := MergedSortedKV{&tx.updates, &kv.mem}.Iter()

    for ; err == nil && iter.Valid(); err = iter.Next() {
        merged.Push(iter.Key(), iter.Val(), iter.Deleted())
    }
    check(err == nil)

    kv.mem = merged
}
```

This is copy-on-write at the MemTable level:

```text
Before tx2 commit:

tx1 snapshot ───► M0
kv.mem       ───► M0

Build commit result separately:

tx2.updates ─┐
             ├── merge ───► M1
current M0 ──┘

Publish with one assignment:

tx1 snapshot ───► M0
kv.mem       ───► M1
```

The old `M0` remains reachable by `tx1`. New transactions copy `M1`.

## Why the merged iterator is exactly what we need

`MergedSortedKV{&tx.updates, &kv.mem}` follows newest-layer-wins ordering:

```text
highest priority: tx.updates
lower priority:  current kv.mem
```

For example:

```text
kv.mem M0:
    a = 1
    b = 2
    c = 3

tx.updates:
    b = 20
    c = deleted
    d = 4
```

The merged result is:

```text
M1:
    a = 1
    b = 20
    c = deleted
    d = 4
```

`updateMem` writes that result into a fresh `SortedArray` and then publishes it as
the new current MemTable.

## Detailed execution trace

Start with:

```text
kv.mem = M0

M0:
    k1 = v1
    k2 = v2
```

### Step 1: create `tx1`

```go
tx1 := kv.NewTX()
```

Inside `NewTX`:

```go
mem1 := kv.mem
```

State:

```text
tx1.updates = {}
tx1 snapshot = mem1 → M0
kv.mem             → M0
```

Its levels are:

```text
tx1.updates
mem1 snapshot M0
SSTables captured at start
```

### Step 2: create `tx2`

```go
tx2 := kv.NewTX()
```

State:

```text
tx1 snapshot → M0
tx2 snapshot → M0
kv.mem       → M0
```

### Step 3: stage changes in `tx2`

```go
tx2.Del([]byte("k1"))
tx2.Set([]byte("k2"), []byte("xxx"))
```

State:

```text
tx2.updates:
    k1 = tombstone
    k2 = xxx

tx2 snapshot:
    k1 = v1
    k2 = v2
```

Because `tx2.updates` has higher priority than its snapshot:

```text
tx2.Get(k1) → not found
tx2.Get(k2) → xxx
```

This preserves the existing “read your own writes” behavior.

### Step 4: `tx1` remains unchanged before commit

`tx1` has no private updates, so it reads M0:

```text
tx1.Get(k1) → v1
tx1.Get(k2) → v2
```

### Step 5: commit `tx2`

The 0805 commit protocol remains in place:

```text
tx2.Commit
  └── KV.applyTX
       ├── updateLog
       │    ├── write DEL k1
       │    ├── write ADD k2=xxx
       │    ├── write COMMIT
       │    └── fsync
       └── updateMem
            ├── merge tx2.updates + M0 into M1
            └── kv.mem = M1
```

State after commit:

```text
tx1 snapshot → M0: {k1=v1, k2=v2}
kv.mem       → M1: {k1=deleted, k2=xxx}
```

### Step 6: compare old and new transactions

`tx1` still uses M0:

```text
tx1.Get(k1) → v1
tx1.Get(k2) → v2
```

A direct `kv.Get` creates a new transaction after the commit, so it snapshots M1:

```text
kv.Get(k1) → not found
kv.Get(k2) → xxx
```

Both results are correct for their respective start times.

## Nested transactions and snapshots

0901 retains the nested-transaction design from 0805:

```go
func (tx *KVTX) NewTX() *KVTX {
    inner := &KVTX{target: tx}
    inner.levels = slices.Concat(
        MergedSortedKV{&inner.updates},
        tx.levels,
    )
    return inner
}
```

The inner transaction inherits the outer transaction's levels:

```text
inner.updates
outer.updates
outer MemTable snapshot
outer SSTable snapshot
```

It does not take a new view of the current database. That is essential: a nested row
operation must remain inside the snapshot of its enclosing SQL statement.

```text
Outer statement starts with M0
Another transaction commits M1
Inner row transaction starts

Inner must inherit M0 from outer—not capture M1.
```

Otherwise one SQL statement could observe multiple database versions.

## The chapter test as a specification

The solution adds `TestKVSnapshot` to `kv_test.go`. Its important sequence is:

```go
kv.Set("k1", "v1")
kv.Set("k2", "v2")

tx1, tx2 := kv.NewTX(), kv.NewTX()

tx2.Del("k1")
tx2.Set("k2", "xxx")
tx2.Commit()
```

The required observations are:

| Reader | `k1` | `k2` | Why |
| --- | --- | --- | --- |
| `tx2`, before commit | missing | `xxx` | Reads its own updates |
| `kv`, after `tx2` commit | missing | `xxx` | New transactions see M1 |
| `tx1`, after `tx2` commit | `v1` | `v2` | Existing transaction stays on M0 |

This single test checks three important properties:

1. Read your own writes.
2. Publish committed changes to new transactions.
3. Preserve old snapshots for transactions already in progress.

## Isolation levels: where 0901 fits

Terminology varies across database products, so behavior matters more than labels.

| Level/concept | What a transaction may observe | Typical anomaly |
| --- | --- | --- |
| Read uncommitted | Another transaction's uncommitted writes | Dirty reads |
| Read committed | Only committed values, but each read may see a newer commit | Non-repeatable reads, read skew |
| Repeatable read / snapshot isolation | One stable committed snapshot | Write skew may remain |
| Serializable | Effect equivalent to some one-at-a-time ordering | Prevents concurrency anomalies when correctly implemented |

### Dirty read

```text
tx1 writes k=new but has not committed
tx2 reads k=new
tx1 aborts
```

`tx2` observed a value that never became committed.

Our private `KVTX.updates` already prevents this: one top-level transaction cannot
read another transaction's private write set.

### Non-repeatable read

```text
tx1 reads k=old
tx2 writes k=new and commits
tx1 reads k=new
```

0901 prevents this by keeping `tx1` on its starting snapshot.

### Read skew

```text
Initial: account A=500, account B=500

tx1 reads A=500
tx2 transfers 100 from B to A and commits
tx1 reads B=400

tx1 calculates total=900, although committed totals were always 1000.
```

0901 prevents this because both reads use the same starting snapshot:

```text
tx1 reads A=500 and B=500
```

### Phantom in a read-only repeated query

```text
tx1: SELECT all users WHERE age >= 18 → Alice, Bob
tx2: INSERT Carol, age 20; COMMIT
tx1: repeat query
```

With a complete stable snapshot, `tx1` should still see Alice and Bob, not Carol.
Our range iterators read through the transaction's captured levels, so the intended
0901 behavior is a stable result set.

## Critical nuance: snapshot isolation is not serializability

The chapter introduces snapshot isolation while discussing the strongest isolation
goal. Kleppmann's distinction is important:

```text
Stable snapshot reads ≠ serializable execution
```

Serializable means the final effect must match some serial ordering:

```text
Either tx1 ran completely before tx2,
or tx2 ran completely before tx1.
```

Ordinary snapshot isolation can produce outcomes that match neither ordering.

### Lost update: not solved by 0901

Initial state:

```text
counter = 10
```

Two transactions start from the same snapshot:

```text
tx1 reads 10, computes 11
tx2 reads 10, computes 11

tx1 commits counter=11
tx2 commits counter=11
```

Final result:

```text
counter = 11
```

The correct result after two increments should be 12. One update was lost.

0901 gives each transaction a stable snapshot but does not yet check whether another
transaction modified the same key after that snapshot. Chapter 0902 adds write-write
conflict detection and abort/retry behavior.

### Write skew: not solved by ordinary snapshot isolation

Suppose Alice and Bob are the only doctors on call. The invariant is:

```text
At least one doctor must remain on call.
```

Two transactions start from the same snapshot:

```text
tx1 reads: Alice=true, Bob=true
tx2 reads: Alice=true, Bob=true

tx1 decides it is safe to set Alice=false
tx2 decides it is safe to set Bob=false
```

They update different keys, so simple same-key conflict detection does not notice a
collision. Both may commit:

```text
Alice=false
Bob=false
```

Each transaction saw a consistent snapshot, but together they violated the invariant.
Preventing this generally requires true serializable isolation, predicate/range locks,
or SSI-style tracking of dependencies between reads and writes.

### Why the distinction matters for this project

Strictly speaking, 0901 implements the **stable-read snapshot mechanism**. A common
formal definition of snapshot isolation also requires aborting write-write conflicts;
that arrives in 0902. Even after 0902, same-key conflict detection alone should not be
confused with a complete SSI implementation that detects write skew and predicate
conflicts.

Use this vocabulary when reviewing the code:

```text
0901: consistent point-in-time reads
0902: optimistic same-key update conflict detection
Full serializability: a broader guarantee, not completed by 0901 alone
```

## Alternative implementation strategies

The chapter lists several ways to preserve a transaction's MemTable view.

### 1. Copy the MemTable when the transaction begins

```text
NewTX cost: proportional to MemTable size if deep copied
Commit cost: normal mutation
```

Simple, but starting many transactions could become expensive.

### 2. Replace the MemTable on commit

```text
NewTX: keep old MemTable snapshot
Commit: construct and publish a new MemTable
```

This is the approach used by 0901. The implementation combines a cheap struct copy
at `NewTX` with rebuilding/swapping at commit.

### 3. Use a persistent copy-on-write data structure

Only modified paths are copied; unchanged structure is shared. This is conceptually
similar to append-only/copy-on-write B-trees that create a new root for each version.

### 4. Make the MemTable itself an LSM-like set of immutable layers

Instead of one mutable array, add immutable in-memory runs and merge them later.
Transactions can retain the run set that existed at their start.

### 5. Version every MemTable key

Each version records a transaction ID or timestamp. Reads apply visibility rules:

```text
Ignore versions created after my snapshot.
Ignore aborted versions.
Choose the newest version visible to me.
```

This is closer to a general MVCC implementation, but requires version metadata,
visibility checks, tracking active transactions, and garbage collection.

## Copy-on-write versus general MVCC

0901 uses copy-on-write snapshots, not per-record transaction IDs.

```text
0901 copy-on-write:
    snapshot = references to an older MemTable/SSTable set

General MVCC:
    snapshot = timestamp/transaction ID
    records = multiple tagged versions
    reads = visibility-rule filtering
```

Both can provide a stable point-in-time view, but their mechanics differ.

Kleppmann's PostgreSQL example keeps multiple row versions with creation/deletion
transaction IDs. The 0901 engine gets a similar read effect more simply because its
storage already uses immutable sorted layers and its MemTable is intentionally small.

## Why readers and writers no longer interfere

After 0901:

```text
Reader tx1 ───► old M0 snapshot

Writer tx2 builds M1 separately
                 │
                 ▼
              kv.mem = M1
```

The reader does not need to block the writer. The writer does not modify the reader's
snapshot.

This captures an important MVCC principle:

```text
Readers do not block writers.
Writers do not block readers.
```

That principle applies to read visibility. Concurrent writers still need conflict
handling and safe synchronization, which are later steps.

## Memory lifetime and cleanup

An old snapshot must remain alive while any transaction can still read it:

```text
tx1 ───► M0
kv  ───► M1
```

Because `tx1` references M0, Go's garbage collector cannot reclaim the relevant
arrays yet. After `tx1` ends and no other transaction references M0, the old snapshot
can become unreachable and be reclaimed.

This produces an important real-world trade-off:

```text
Long-running transaction
    → old versions remain reachable longer
    → greater memory/disk retention
```

General MVCC systems have the same fundamental issue and need vacuuming or version
garbage collection based on the oldest active snapshot.

## Performance trade-offs

The 0901 design favors simplicity:

### Benefits

- Stable reads without read locks
- Existing transactions do not observe later commits
- New transactions immediately see the latest committed MemTable
- Nested transactions inherit the correct outer snapshot
- Old snapshots are reclaimed automatically when no longer referenced
- Time complexity remains acceptable because the MemTable has a configured size limit

### Costs and assumptions

- Every commit rebuilds the current MemTable
- Long-running transactions retain old MemTable arrays
- Entries are treated as immutable after publication
- This chapter does not yet make concurrent goroutine access data-race-free
- Background compaction and snapshot lifetime need careful synchronization later
- Write conflicts are not detected in 0901

The book accepts the rebuilding cost because the MemTable is bounded and represents
only the newest part of the LSM tree.

## Locks versus snapshots

The simplest way to guarantee serializable behavior is to run one transaction at a
time:

```text
tx1: begin ───────── commit
tx2:                       begin ───────── commit
```

This avoids concurrency anomalies but sacrifices concurrency.

Finer-grained locks permit unrelated operations to overlap:

```text
tx1 locks key A
tx2 locks key B
```

However, acquiring multiple locks in different orders can deadlock:

```text
tx1 holds A, waits for B
tx2 holds B, waits for A
```

A database must detect the cycle, abort one transaction, and let the application retry.

Snapshot/MVCC designs instead preserve old versions for readers. They improve read
concurrency, but stable versions alone do not settle conflicting writes or guarantee
serializability.

## What changes in 0901

The intended implementation work is deliberately narrow.

### `kv.go`

Modify:

```text
KV.NewTX
    Copy kv.mem into a transaction-local snapshot.

KV.updateMem
    Merge updates with current mem into a fresh SortedArray.
    Replace kv.mem with the new array.
```

No table-layer behavior needs to change for this chapter. `DBTX` automatically gains
snapshot reads because it delegates all storage reads to its `KVTX`.

### `kv_test.go`

Add the solution's `TestKVSnapshot`, which verifies:

```text
tx1 keeps old values
tx2 reads its own changes
tx2 commit updates the public database
tx1 remains on its original snapshot after tx2 commit
```

### Package preparation

Because the new folder was copied from 0805, all Go files must use:

```go
package db0901
```

rather than:

```go
package db0805
```

The implementation should otherwise remain close to 0805. This chapter is about one
specific behavioral change, not a broad refactor.

## What 0901 guarantees

After implementation, the intended guarantees are:

- A transaction reads a stable MemTable/SSTable view captured at its start.
- A transaction reads its own writes before consulting that snapshot.
- Commits become visible to transactions that start afterward.
- Commits do not change the read view of transactions already in progress.
- Nested transactions remain inside the outer transaction's snapshot.
- Atomic WAL commit behavior from 0805 remains intact.

## What 0901 does not guarantee

This chapter does **not** yet provide:

- Write-write conflict detection
- Lost-update prevention
- Write-skew prevention
- Predicate conflict detection
- Full serializability
- Thread-safe simultaneous goroutine access
- Deadlock detection
- Locks or `SELECT ... FOR UPDATE`
- A complete per-record MVCC version/timestamp system

These boundaries matter. A stable snapshot is a major isolation improvement, but it
is one component of concurrency control rather than the end of the subject.

## Invariants to preserve while implementing

1. `tx.updates` remains the highest-priority read layer.
2. A transaction never points directly at the live mutable `kv.mem` field.
3. Commit never mutates a MemTable that an older transaction may still reference.
4. WAL commit occurs before publishing the new MemTable.
5. Nested transactions inherit the outer transaction's snapshot.
6. SSTables remain ordered newest-to-oldest behind the MemTable snapshot.
7. Deletes remain tombstones until it is safe for compaction to remove them.

## Review questions

1. Why did `&kv.mem` allow a transaction's view to change?
2. Why is `mem := kv.mem` alone insufficient if commits still call `kv.mem.Set`?
3. Why must `updateMem` build a fresh `SortedArray`?
4. Why are immutable SSTables naturally useful as snapshots?
5. Why do transaction-local updates come before the captured snapshot?
6. Why should an inner transaction inherit its outer transaction's snapshot?
7. Which anomaly does the `TestKVSnapshot` interleaving demonstrate?
8. Why can two counter increments still lose an update after 0901?
9. Why can write skew occur even if two transactions update different keys?
10. What is the difference between snapshot isolation and serializable isolation?

## Final mental model

```text
At tx1 start:

tx1.updates ───► {}
tx1.snapshot ──► M0
kv.mem ─────────► M0

tx2 commits:

tx2.updates + M0 ──merge──► M1
                              │
                              ▼
                           kv.mem

tx1 still reads:

tx1.updates ───► {}
tx1.snapshot ──► M0

new tx3 reads:

tx3.updates ───► {}
tx3.snapshot ──► M1
```

The one-sentence takeaway is:

> 0901 stops a transaction's committed-data view from moving underneath it by
> retaining the old MemTable and publishing each commit as a new MemTable.
