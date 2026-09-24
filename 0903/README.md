# Chapter 0903: Multithread & Locks

## What this chapter is trying to accomplish

Our transaction engine already has three important behaviors:

```text
0805  A transaction's writes commit together through the WAL.
0901  A transaction keeps reading its starting snapshot.
0902  A stale same-key write is rejected at commit.
```

So far, we could demonstrate overlapping transactions by alternating calls on one Go
thread. Chapter 0903 introduces synchronization for different goroutines calling the
engine at the same time. The aim is to keep shared engine state coherent across those
interleavings.

The chapter's immediate targets are:

1. `NewTX` must capture a consistent snapshot and register the transaction.
2. Top-level commits must append to the WAL in a defined order.
3. The committed MemTable, commit number, and conflict history must be published
   together as one coherent in-memory state.
4. Aborting or finishing a transaction must update the active-transaction list safely.

The tool is `sync.Mutex`. The final design uses **two mutexes** so the database can
admit new readers while a writer is doing disk I/O.

## First, what does “multithreaded” mean here?

The CPU may interleave instructions from several threads or execute them in parallel
on different cores. Hardware multithreading describes how a processor runs thread
instructions; Go goroutines are software tasks scheduled onto operating-system
threads. For this chapter, the important result is that two goroutines can access
the same `*KV` at overlapping times. Their exact order is not guaranteed.

```go
go func() { tx1 := kv.NewTX(); /* use tx1 */ }()
go func() { tx2 := kv.NewTX(); /* use tx2 */ }()
```

Even on a single CPU core, one goroutine can be paused between two operations and
another can run. Having multiple cores only adds more possible overlaps. Our code
must be correct for all allowed schedules.

There are two related but distinct kinds of concurrency in this project:

```text
Logical transaction overlap:
    tx1 begins → tx2 begins → tx2 commits → tx1 finishes

Simultaneous execution:
    goroutine A is inside NewTX while goroutine B is inside Commit
```

0901 and 0902 dealt with the first kind. 0903 introduces protection for shared Go
memory and WAL access during the second kind.

## Why the 0902 code needs synchronization

The shared `KV` object contains state that many transactions use:

```go
type KV struct {
    log      Log
    mem      SortedArray
    main     []SortedFile
    snapshot uint64
    history  []UpdatedKey
    ongoing  []*KVTX
    // ...
}
```

Without coordination, operations on these fields can overlap in unsafe ways.

### Race 1: transaction start versus commit publication

`NewTX` must capture the MemTable version and the matching commit timestamp.
Imagine a writer publishes a new MemTable between those two reads:

```text
Writer:       old state (M0, timestamp 7)
Reader:       captures timestamp 7
Writer:       publishes M1, timestamp 8
Reader:       captures M1
```

The reader now has `(M1, 7)`, a pair that never represented one committed version.
Its data view and conflict-check timestamp disagree. `NewTX` must read the pair and
register the transaction while publication is excluded.

### Race 2: two commits sharing the WAL

`updateLog` writes multiple entries and a commit marker. If two transactions write
to the same log concurrently, their records can interleave:

```text
Wanted:
    A1, A2, commit-A, B1, B2, commit-B

Possible without serialized writers:
    A1, B1, A2, commit-B, B2, commit-A
```

The second sequence does not preserve the intended transaction boundaries.
Competing commits need one defined order through validation, WAL writing, and
publication.

### Race 3: active transactions and history

`NewTX` appends to `kv.ongoing`; commit and abort remove from it. A commit may append
to `kv.history`, while a finishing transaction may prune that slice. Go slices are
not automatically safe for simultaneous reads and writes. A mutex must protect the
shared operations.

### Race 4: exposing a partly published commit

The intended successful commit order is:

```text
WAL commit → new MemTable → new snapshot number → updated-key history
```

Another goroutine must not begin a transaction in the middle of the in-memory
publication and capture only some of those changes.

## What a mutex does

A mutex is a lock that grants one goroutine at a time access to a protected section
of code:

```go
mu.Lock()
// Access shared state.
mu.Unlock()
```

If goroutine A holds `mu`, goroutine B waits at `mu.Lock()` until A unlocks. In Go,
the zero value of `sync.Mutex` is ready to use. Unlocking also makes the protected
writes visible to a later goroutine that successfully locks the same mutex.

We commonly write:

```go
kv.mu.Lock()
defer kv.mu.Unlock()
```

The `defer` releases the lock when the function returns, including early returns.
The code from `Lock()` until `Unlock()` is the **critical section**.

The mutex works only when all access paths follow the same locking rule. Protecting
one writer but leaving another writer or reader unprotected does not make a field
safe.

## What is actually locked?

The lock is held during specific function sections, **not for the whole lifetime of
a transaction**.

```text
NewTX:     acquire briefly → capture state → register → release

Transaction body:
           read snapshot and stage private updates; no global lock held

Commit:    acquire → validate/write/publish → release

Abort:     acquire briefly → untrack → release
```

Thus, two transactions can remain open and do private work concurrently. The
engine coordinates their entry and exit points.

This distinction matters when comparing our mutexes with database row locks:
`sync.Mutex` protects Go code and shared data structures. A database transaction
lock, such as a row lock under two-phase locking, can be held across the entire
transaction to enforce a logical isolation guarantee. We are using snapshots and
commit-time conflict checks for transaction isolation.

## A simple first design: one mutex

The book first considers one mutex around `NewTX`, top-level commit, and abort:

```go
type KV struct {
    // ...
    mu sync.Mutex
}

func (kv *KV) NewTX() *KVTX {
    kv.mu.Lock()
    defer kv.mu.Unlock()
    // Capture the snapshot and register the transaction.
}

func (kv *KV) applyTX(tx *KVTX) error {
    kv.mu.Lock()
    defer kv.mu.Unlock()
    // Validate, write WAL, publish state, and untrack.
}
```

This is easy to reason about: only one goroutine enters these sections at a time.
It also makes disk I/O part of the locked section. `updateLog` can take time to write
and sync the WAL. While that happens, a read-only transaction cannot even enter
`NewTX` to capture its existing snapshot.

```text
Writer A:  lock mu ───── WAL write/fsync ───── publish ── unlock
Reader B:            wait at NewTX(mu) ─────────────────► begin
```

The reader does not need the writer's new value. It could read the old immutable
version, so this waiting is avoidable.

## The chapter's design: two mutexes

The final 0903 design puts both mutexes on `KV`:

```go
type KV struct {
    // ...
    mu     sync.Mutex
    commit sync.Mutex
}
```

They protect different things:

| Mutex | Main job | Held during |
|---|---|---|
| `kv.commit` | Give top-level commits one order; prevent WAL writes from interleaving | Conflict check, WAL write, publication, commit cleanup |
| `kv.mu` | Protect the shared in-memory transaction state | `NewTX`, abort/untrack, MemTable and history publication |

Think of `commit` as the **writer queue** and `mu` as the **short shared-state gate**.
Both are ordinary Go mutexes. Their names describe their roles, not special Go types.

### Why not use only `kv.commit`?

`NewTX` and abort also access `kv.snapshot`, `kv.mem`, `kv.main`, `kv.ongoing`, or
`kv.history`. They do not all take the commit mutex. A second mutex protects the
shared state they touch.

### Why not keep `kv.mu` during WAL I/O?

WAL I/O may be slow. Holding `kv.mu` then would make every new transaction wait to
capture a snapshot, even though the old copy-on-write MemTable is still readable.
The short `kv.mu` section lets the engine admit readers during the disk phase.

## The planned code path

### `KV.NewTX`: capture a coherent starting point

```go
func (kv *KV) NewTX() *KVTX {
    kv.mu.Lock()
    defer kv.mu.Unlock()

    tx := &KVTX{snapshot: kv.snapshot, target: kv}
    mem := kv.mem
    tx.levels = MergedSortedKV{&tx.updates, &mem}
    for i := range kv.main {
        tx.levels = append(tx.levels, &kv.main[i])
    }
    kv.ongoing = append(kv.ongoing, tx)
    return tx
}
```

The lock covers the numeric snapshot, the captured MemTable, the SSTable list, and
the active-transaction registration. The actual reads and writes through `tx` happen
after `NewTX` returns.

### `KV.applyTX`: order top-level commits

The significant lock placement is:

```go
func (kv *KV) applyTX(tx *KVTX) error {
    kv.commit.Lock()
    defer kv.commit.Unlock()
    defer kv.untrackTXSync(tx)

    if tx.updates.Size() == 0 {
        return nil
    }
    if kv.checkTXConflict(tx) {
        return ErrTXConflict
    }
    if err := kv.updateLog(tx); err != nil {
        return err
    }

    kv.mu.Lock()
    defer kv.mu.Unlock()
    kv.updateMem(tx)
    kv.updateHistory(tx)
    return nil
}
```

The sequence is:

```text
Acquire commit lock
    ├─ check that tx has writes
    ├─ validate against newer committed keys
    ├─ write transaction to the WAL
    └─ acquire mu
          ├─ publish new copy-on-write MemTable
          └─ update commit number and history
       release mu
       untrack finished tx under mu
Release commit lock
```

The commit mutex stays held while the WAL is written, but `kv.mu` is acquired only
for the short in-memory publication. This means another goroutine may run `NewTX`
during the WAL phase and capture the old committed state. It cannot begin while
publication is halfway through.

`defer` runs in last-in, first-out order. In this function, the deferred `kv.mu.Unlock()`
runs before `untrackTXSync(tx)`, and that cleanup runs before `kv.commit.Unlock()`.
This avoids trying to lock `kv.mu` twice from the same goroutine.

### `KV.abortTX` and cleanup

Top-level abort removes the transaction from `kv.ongoing`:

```go
func (kv *KV) abortTX(tx *KVTX) {
    kv.untrackTXSync(tx)
}

func (kv *KV) untrackTXSync(tx *KVTX) {
    kv.mu.Lock()
    defer kv.mu.Unlock()
    // Remove tx from ongoing and prune history no active tx needs.
}
```

The same synchronized cleanup runs after a top-level commit, including an early
return for a read-only transaction, a detected conflict, or a WAL error. Nested
transactions still merge into their parent; they do not enter the global `KV` commit
path until the parent commits.

## Detailed execution: a reader arrives during a slow commit

Start with:

```text
kv.snapshot = 7
kv.mem = M0: {k1=old, k2=blue}
```

Transaction `writer` has already begun and staged `k1=new`. Transaction `reader`
will begin while the writer is committing.

### Moment 1: the writer enters `applyTX`

```text
writer holds kv.commit
writer checks for conflicts
writer starts updateLog and waits on disk sync

writer does not hold kv.mu during disk I/O
```

### Moment 2: the reader calls `NewTX`

```text
reader acquires kv.mu
reader captures snapshot number 7
reader captures MemTable M0
reader is added to kv.ongoing
reader releases kv.mu
```

The reader is free to read `k1=old` from its captured `M0` while the writer's WAL
operation continues. The writer has not published the new MemTable yet.

### Moment 3: the writer finishes the WAL operation

```text
writer acquires kv.mu
writer builds/publishes M1: {k1=new, k2=blue}
writer advances kv.snapshot from 7 to 8
writer records (8, k1) in history because reader is ongoing
writer releases kv.mu
```

Now:

```text
reader.snapshot = 7
reader's data view ──► M0: k1=old
kv.mem ──────────────► M1: k1=new
kv.snapshot = 8
```

`reader` may continue reading `old`. If it later stages a write to `k1`, its
0902 conflict check sees `(8, k1)`, which is newer than its starting snapshot `7`,
and rejects the stale write. A transaction starting after publication captures `M1`
and timestamp `8` together.

### Moment 4: writer exits

The deferred cleanup briefly takes `kv.mu` to remove `writer` from `kv.ongoing`
and prune obsolete history. Then it releases `kv.commit`, allowing the next writer
to validate and append its WAL records.

The useful concurrency is:

```text
writer: [commit lock ── WAL I/O ── mu: publish ── cleanup ── release]
reader:                 [mu: begin] ── read old snapshot ───────────►
```

## Which operations can wait?

| Operation | Lock it needs | What can delay it? |
|---|---|---|
| Start a transaction | `kv.mu` briefly | A simultaneous snapshot or publication section |
| Read an existing snapshot | No `KV` lock in this path | Work on the transaction's own iterator/data |
| Stage private updates | No `KV` lock in this path | Work inside that transaction |
| Top-level write commit | `kv.commit`; later `kv.mu` | Another commit; brief publication contention |
| Top-level abort | `kv.mu` briefly | Another shared-state section |
| Top-level read-only `Commit()` | `kv.commit`, then cleanup under `kv.mu` | A writer holding `kv.commit`, including during WAL I/O |

That last row is a subtle detail of the chapter's sample code. Read-only
transactions can **begin**, read, and abort while a writer is doing WAL I/O. A
read-only transaction that calls `Commit()` still waits for `kv.commit`, because
the early return for empty updates occurs after acquiring that mutex.

## How this relates to database locking in DDIA

Database books also use the word *lock* for transaction isolation. Keep the lock's
scope and lifetime in mind.

| Mechanism | Protected thing | Typical lifetime | Our engine |
|---|---|---|---|
| Go `sync.Mutex` | A shared in-process code/data section | Until `Unlock()` | `kv.mu` and `kv.commit` in 0903 |
| Database row or range lock | A logical record or predicate | Potentially until transaction end | Not introduced in 0903 |
| Snapshot + conflict validation | The transaction's read view and committed key conflicts | Entire transaction plus commit check | 0901 and 0902 |

Under **two-phase locking**, a database may hold shared or exclusive locks on rows
or ranges until the transaction ends. Readers and writers can wait for one another,
and several locks can form a deadlock. That approach can provide serializability when
implemented with the required predicate or range protection.

Our `kv.mu` does not hold a row lock while a user thinks or while a transaction reads
its snapshot. Instead, a transaction reads an immutable old version and 0902 detects
same-key conflicts at commit. The `kv.commit` mutex orders physical commits and
protects WAL sequencing. These short-lived mutexes do not turn the engine into a
two-phase-locking database or prevent write skew between different keys.

This distinction connects the chapter to DDIA's broader comparison of pessimistic
locking and optimistic validation:

```text
Logical conflict handling:  snapshot + validate at commit
Physical thread safety:     mutexes around shared engine operations
```

Both are needed for this design, and they solve different problems.

## Lock order and deadlocks

When a write commit needs both locks, it acquires them in this order:

```text
kv.commit → kv.mu
```

`NewTX` and abort use only `kv.mu`. Within these transaction paths, nothing holds
`kv.mu` and then waits for `kv.commit`. Keeping a consistent acquisition order
avoids the classic cycle:

```text
goroutine A holds commit, waits for mu
goroutine B holds mu, waits for commit
```

That cycle would deadlock. Any future operation that needs both locks must respect
their order. The compaction work in the next chapter needs a separate lock review.

## What the chapter does and does not establish

The intended achievement is safe coordination of transaction entry, top-level
commit, and abort, with shorter lock holds for readers than a one-mutex design.

The lock placement also has boundaries worth noticing when reviewing code:

- The 0903 solution does not yet synchronize `Compact()` against transaction
  operations; the next chapter revisits compaction and lock usage.
- `checkTXConflict()` scans `kv.history` before taking `kv.mu`, while a concurrent
  `Abort()` can prune `kv.history` under `kv.mu`. A literal implementation of the
  solution therefore needs additional coordination around that read before claiming
  that every commit/abort interleaving is race-free. Holding `kv.mu` during the
  history scan, or arranging cleanup under the commit lock, are possible approaches
  whose lock ordering must be checked carefully.
- Sharing and mutating the *same* `KVTX` from multiple goroutines is a separate
  question; its private `updates` array is not protected by these `KV` mutexes.
- Higher-level `DB` caches and other callers must obey their own synchronization
  rules. Adding mutexes to `KV` alone is not a proof that every public API is safe
  under arbitrary simultaneous calls.

These are concrete boundaries of the chapter's sample lock placement. They do not
change the central lesson: identify every piece of shared state, define the lock that
guards it, and check every path that reads or writes it.

## Implementation map for this chapter

The 0903 solution changes `kv.go` in a small number of places:

1. Import `sync` and add `mu` and `commit` to `KV`.
2. Take `mu` around top-level `NewTX` initialization and registration.
3. Route top-level abort through synchronized untracking.
4. Take `commit` around the top-level `applyTX` path.
5. Take `mu` around MemTable and history publication.
6. Release the locks in a safe order on every return path.

The chapter-specific behavior is about goroutine interleavings, so a normal
single-goroutine test can pass even when a shared-field race remains. When reviewing
an implementation, trace which lock guards each read and write, and use the race
detector for exercised concurrent paths.

## Review questions

1. How can two goroutines race even when only one CPU core is executing them?
2. Which `KV` fields must `NewTX` capture as one consistent starting point?
3. Why can two simultaneous `updateLog` calls corrupt the intended WAL order?
4. What does `kv.commit` protect that `kv.mu` alone would protect too broadly?
5. Why may `NewTX` proceed while a writer is blocked on disk sync?
6. Why does an existing transaction continue reading `M0` after `M1` is published?
7. Why does a read-only `Commit()` still wait behind a slow writer in the shown code?
8. Why are Go mutexes around engine state different from database row locks held to
   the end of a transaction?
9. In which order may code safely acquire `kv.commit` and `kv.mu`?
10. Which history read needs attention if abort can prune history concurrently?
11. Why does passing ordinary sequential tests not establish freedom from data races?

## Final mental model

```text
                    transaction lifetime
          ┌─────────────────────────────────────┐
          │ read old snapshot; stage own writes  │
          └─────────────────────────────────────┘
             ▲                               │
             │                               ▼
        mu: capture                    commit: validate
        and register                   and write WAL
                                           │
                                           ▼
                                     mu: publish
                                     MemTable/history
                                           │
                                           ▼
                                     mu: untrack
                                           │
                                           ▼
                                     release commit
```

`kv.mu` keeps the shared in-memory state coherent. `kv.commit` gives top-level
commits a single WAL and publication order. Transactions can perform their private
work between these protected entry and exit sections.
