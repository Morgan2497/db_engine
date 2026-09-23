# Chapter 0902: Update Conflicts

## Goal of this chapter

Chapter 0901 gave each transaction a stable read snapshot. That solved the problem of
a transaction's view changing while it was running, but it did not make concurrent
writes safe.

Chapter 0902 addresses the next problem:

```text
What if two transactions read the same old value, independently calculate updates,
and then both try to commit?
```

Without conflict detection, the later commit can silently overwrite the earlier one.
This is the **lost update** anomaly.

The goal of 0902 is:

```text
Allow transactions to read and prepare changes concurrently.
At commit time, reject a transaction if one of the keys it wants to write
was already committed by another transaction after its snapshot began.
```

This chapter implements a small form of **optimistic concurrency control (OCC)**.
Transactions proceed without taking semantic locks. Before a commit becomes durable,
the engine validates that its assumptions are still safe.

The progression is:

```text
0805: Commit all writes together.                       Atomicity
0901: Keep a stable view of committed data.             Read snapshot
0902: Reject stale writes to keys changed since start.  Conflict detection
0903: Protect shared state across actual Go threads.    Synchronization
```

The one-sentence goal is:

> 0902 prevents one transaction from silently overwriting a same-key update committed
> by another transaction after the first transaction began.

## Reading map

Page numbers below use the books' printed pagination. A PDF viewer may include front
matter and display a different page number.

### *Build Your Own Database From Scratch in Go*

Read Chapter 0902, **“Update Conflicts,” pages 95–96**.

- Page 95 introduces read-modify-update, lost updates, pessimistic locking, optimistic
  validation, transaction timestamps, update history, and active transactions.
- Page 96 outlines the commit changes: untrack finished transactions, check history for
  conflicts, record successful updates, and prune history that no active transaction
  can need.

### Martin Kleppmann, *Designing Data-Intensive Applications*

The direct companion is Chapter 7, **“Transactions”**:

- **Pages 233–234, “Preventing Lost Updates”**: the lost-update anomaly and common
  read-modify-write examples such as counters, balances, documents, and wiki edits.
- **Page 234, “Atomic Write Operations”**: avoid an application-level read-modify-write
  cycle when the database can perform the whole operation atomically.
- **Pages 234–235, “Explicit Locking”**: pessimistically lock the object before making
  a decision based on it.
- **Pages 235–236, “Automatically Detecting Lost Updates”**: let transactions execute
  concurrently, then abort one when validation discovers a stale update. This is the
  closest match to 0902.
- **Page 236, “Compare-and-Set”**: update only if a value still matches the value that
  was previously read.
- **Pages 237–242, “Preventing Write Skew and Phantoms”**: explains the boundary of
  0902. Same-key write-conflict detection does not provide serializability.

For implementing 0902, pages **233–236** are essential. Pages **237–242** are important
for understanding what this chapter still does not solve.

## The problem: read-modify-update

A read-modify-update operation has three logical steps:

```text
1. Read a value.
2. Calculate a new value from what was read.
3. Write the calculated value.
```

For example, incrementing a counter:

```go
tx := kv.NewTX()
value, _, _ := tx.Get([]byte("counter"))
next := decode(value) + 1
tx.Set([]byte("counter"), encode(next))
tx.Commit()
```

The write depends causally on the earlier read:

```text
read counter=10 ──► calculate 11 ──► write counter=11
```

If the value changes between the read and commit, the calculation may no longer be
valid.

## Lost update: the anomaly 0902 prevents

Assume the counter starts at `10`. Two transactions begin from the same snapshot:

```text
                     tx1                         tx2
                      │                           │
                      ├── read counter = 10       ├── read counter = 10
                      │                           │
                      ├── calculate 11            ├── calculate 11
                      │                           │
                      ├── stage counter = 11      ├── stage counter = 11
                      │                           │
                      ├── commit                  │
                      │                           ├── commit
                      ▼                           ▼
```

Without conflict detection:

```text
Initial:     counter = 10
tx1 commit:  counter = 11
tx2 commit:  counter = 11

Expected after two increments: 12
Actual result:                 11
```

One increment disappeared. The second write was calculated from stale input and
overwrote the first transaction's result.

This can affect more than counters:

- Two withdrawals calculated from the same account balance
- Two users editing and replacing the same document
- Two requests updating the same JSON object
- Two workers claiming the same state transition
- Any application that reads an object, modifies it locally, and writes it back

## Why 0901 snapshot isolation is not enough

0901 intentionally keeps an old snapshot stable:

```text
tx1.snapshot ──► M0: counter=10
tx2.snapshot ──► M0: counter=10
kv.mem ─────────► M0: counter=10
```

After `tx1` commits:

```text
tx1.updates + M0 ──► M1: counter=11

tx2.snapshot ──────► M0: counter=10
kv.mem ────────────► M1: counter=11
```

The stable snapshot is working correctly: `tx2` continues seeing `10`. But the
calculation `10 + 1` is now stale. If 0901 simply merges `tx2.updates` over the current
MemTable, it creates another version containing `11`:

```text
tx2.updates(counter=11) + M1(counter=11) ──► M2(counter=11)
```

The read snapshot prevented inconsistent reads, but it did not validate the later
write. That validation is the purpose of 0902.

## Two broad concurrency-control strategies

### Pessimistic concurrency control

Pessimistic control assumes interference is likely and prevents it in advance:

```text
tx1 locks counter
tx1 reads and updates counter
tx1 commits and unlocks
tx2 may now continue
```

This preserves the read-to-write dependency by preventing another writer from changing
the object during the operation. The costs include waiting, lock bookkeeping, and
possible deadlocks when transactions acquire multiple locks in different orders.

### Optimistic concurrency control

Optimistic control assumes conflicts are uncommon:

```text
tx1 and tx2 run without locking each other
                 │
                 ▼
validate at commit
                 │
          ┌──────┴──────┐
          ▼             ▼
      no conflict    conflict
         commit         abort and retry
```

0902 chooses this approach. Work may be discarded when a conflict occurs, but
conflict-free transactions do not wait for one another merely because their lifetimes
overlap.

| Question | Pessimistic locking | 0902 optimistic validation |
|---|---|---|
| When is danger handled? | Before or during access | At commit |
| Do transactions wait? | Often | Not for semantic key locks |
| Can work be discarded? | Less often | Yes, on conflict |
| Typical failure | Waiting or deadlock | Abort and retry |
| 0902 implementation? | No | Yes |

## The central validation rule

Every top-level transaction receives a logical start timestamp. Every successful
write commit receives a newer commit timestamp.

At commit time, for every key the transaction wants to change, ask:

```text
Was this same key committed at a timestamp newer than my start timestamp?
```

Formally, a conflict exists when both conditions hold:

```text
historyEntry.snapshot > tx.snapshot
historyEntry.key      == tx.updatedKey
```

If the answer is yes:

```text
return ErrTXConflict
do not write the WAL
do not publish a new MemTable
application must retry using a new transaction
```

If the answer is no, the normal 0805/0901 commit can proceed.

## “Snapshot” has two related meanings in this code

This terminology is easy to confuse.

### The data snapshot from 0901

The transaction's `levels` retain the MemTable/SSTable state used for reads:

```text
tx.levels = [tx.updates, captured MemTable, SSTables...]
```

This answers:

```text
Which data versions may this transaction read?
```

### The numeric snapshot introduced in 0902

`KV.snapshot` is a monotonically increasing commit sequence number, and
`KVTX.snapshot` records its value when the transaction begins.

This answers:

```text
Which commits happened after this transaction began?
```

The numeric field is not a copy of the database:

```text
data snapshot:    captured read layers such as M0
numeric snapshot: uint64 timestamp such as 7
```

They work together: the data snapshot provides stable reads, while the numeric snapshot
allows commit-time conflict validation.

## New transaction metadata

Chapter 0902 adds three fields to `KV`:

```go
snapshot uint64
history  []UpdatedKey
ongoing  []*KVTX
```

It adds a history record:

```go
type UpdatedKey struct {
    snapshot uint64
    key      []byte
}
```

And every top-level `KVTX` records its starting timestamp:

```go
snapshot uint64
```

Their responsibilities are:

| Field | Meaning |
|---|---|
| `kv.snapshot` | Timestamp of the latest successful write commit |
| `tx.snapshot` | Timestamp visible when this transaction began |
| `kv.history` | Keys written by commits that active transactions may need to validate against |
| `kv.ongoing` | Top-level transactions that have not committed or aborted |

The history records keys, not old values. The old values already remain available through
0901's copy-on-write snapshots. For 0902 validation, the engine only needs to know
whether a relevant key changed and when.

## Complete transaction lifecycle

```text
KV.NewTX()
    │
    ├── copy kv.snapshot into tx.snapshot
    ├── capture the 0901 read snapshot
    └── append tx to kv.ongoing
             │
             ▼
      application reads and stages writes
             │
       ┌─────┴─────┐
       ▼           ▼
    Abort()      Commit()
       │           │
       │           ├── defer untrackTX(tx)
       │           ├── if read-only: finish
       │           ├── checkTXConflict(tx)
       │           ├── updateLog(tx)
       │           ├── updateMem(tx)
       │           └── updateHistory(tx)
       │
       └── untrackTX(tx)
             │
             ▼
       prune obsolete history
```

## Detailed execution example

Assume the database has already committed:

```text
kv.snapshot = 5
kv.mem: k1 = original
kv.history = []
```

### Step 1: two transactions start

```go
tx1 := kv.NewTX()
tx2 := kv.NewTX()
```

Both copy the current timestamp:

```text
tx1.snapshot = 5
tx2.snapshot = 5

kv.ongoing = [tx1, tx2]
```

Both also retain the same 0901 data snapshot:

```text
tx1 data snapshot ──┐
tx2 data snapshot ──┼──► M0: k1=original
kv.mem ──────────────┘
```

### Step 2: both update the same key

```text
tx1.updates: k1 = x
tx2.updates: k1 = y
```

Neither transaction modifies the committed database yet.

### Step 3: `tx1` commits

Conflict validation scans `kv.history`, which is empty:

```text
checkTXConflict(tx1) = false
```

The commit proceeds:

```text
1. WAL records k1=x and commit marker.
2. Copy-on-write publishes M1 containing k1=x.
3. kv.snapshot increments from 5 to 6.
4. History records UpdatedKey{snapshot: 6, key: k1}.
5. tx1 is removed from ongoing.
```

State afterward:

```text
kv.snapshot = 6
kv.mem: k1 = x
kv.history: [(6, k1)]
kv.ongoing: [tx2]

tx2 still reads its old snapshot: k1=original
```

### Step 4: `tx2` tries to commit

`checkTXConflict(tx2)` compares its update keys with history:

```text
tx2.snapshot = 5
tx2 updated key = k1

history entry:
    snapshot = 6
    key      = k1
```

Both conflict conditions are true:

```text
6 > 5       true: commit happened after tx2 began
k1 == k1    true: both transactions wrote the same key
```

The commit returns:

```go
ErrTXConflict
```

No WAL entry or MemTable version is produced for `tx2`. Its stale `k1=y` does not
overwrite `tx1`'s `k1=x`.

After `tx2` is untracked, no top-level transactions remain, so the history can be
cleared.

## Why different-key updates can both commit

Suppose:

```text
tx1.updates: a = 10
tx2.updates: b = 20
```

After `tx1` commits, history contains `(newTimestamp, a)`. When `tx2` validates `b`:

```text
a == b    false
```

There is no same-key write conflict, so `tx2` may commit. Its 0901 copy-on-write merge
uses the latest `kv.mem`, preserving `tx1`'s `a=10` while adding `b=20`.

```text
M0: a=1,  b=2
tx1 commit ──► M1: a=10, b=2
tx2 commit ──► M2: a=10, b=20
```

This is the benefit of key-level validation: transactions that write independent keys
do not conflict merely because their lifetimes overlap.

## Commit order matters

The top-level commit order is intentionally:

```text
1. Validate conflicts.
2. Write and commit the WAL.
3. Publish the copy-on-write MemTable.
4. Increment the timestamp and record updated keys.
5. Untrack the transaction and prune history.
```

Why validate first?

```text
A rejected transaction must not leave committed WAL records.
```

Why update history only after the data commit succeeds?

```text
History describes successful committed changes, not attempted changes.
```

Why defer untracking?

```text
The transaction must be removed whether it succeeds, conflicts, is read-only,
or encounters a commit error.
```

## `checkTXConflict`: what it actually checks

The algorithm iterates over every key in `tx.updates`. For each key, it scans the
retained history:

```text
for every key this transaction will write:
    for every retained committed update:
        if committed timestamp is newer than transaction start
           and keys are equal:
            conflict
```

This is optimistic validation, not value comparison. It does not ask whether the final
bytes happen to be equal. If two overlapping transactions both write the same key, the
older transaction's assumptions may be stale, so the later attempted commit is rejected.

Deletes are also writes. A tombstone in `tx.updates` participates in exactly the same
key-conflict check as a set operation.

The simple implementation has roughly this validation cost:

```text
number of transaction update keys × retained history length
```

That is acceptable for this educational engine. A production system might index history
by key, attach versions directly to records, or use a more sophisticated transaction
manager.

## `updateHistory`: assigning commit timestamps

After a successful write commit:

```text
kv.snapshot++
```

Every updated key is then associated with that new timestamp when another active
transaction may need the information.

Example:

```text
commit timestamp 8 updates a and c

history:
    (8, a)
    (8, c)
```

A transaction that began at timestamp `7` conflicts if it also tries to write `a` or
`c`. A transaction beginning at timestamp `8` does not consider those entries newer
than its snapshot.

If no other top-level transaction is active, recording the keys is unnecessary. A future
transaction will begin with the incremented timestamp and therefore cannot be stale
relative to that commit.

## Why `ongoing` transactions are tracked

Without cleanup, `history` would grow forever:

```text
commit 1 keys
commit 2 keys
commit 3 keys
...
```

Only transactions that started before a commit can conflict with that commit. Once all
such transactions have finished, the corresponding history is no longer useful.

`kv.ongoing` lets the engine identify the oldest active transaction:

```text
ongoing snapshots: [12, 15, 18]
oldest active snapshot: 12
```

History older than what active transactions may need can be pruned. If no active
transactions remain, all history can be cleared:

```text
kv.ongoing = []
kv.history = []
```

The implementation keeps transactions in start order because they are appended by
`NewTX`, and deleting a completed transaction preserves the relative order of the rest.

### Long-running transaction cost

A long-running transaction retains an old start timestamp:

```text
long tx snapshot = 10
current snapshot = 1000
```

The engine must retain enough history to validate that old transaction. Thus, one slow
transaction can prevent history cleanup and increase memory use and validation time.
This is a common multiversion/concurrency-control tradeoff: old active transactions delay
garbage collection of metadata or versions.

## Commit, abort, and read-only behavior

### Successful write commit

```text
validate → WAL → MemTable → history → untrack
```

### Conflict

```text
validate → ErrTXConflict → untrack
```

The transaction is finished. The application must open a new transaction, repeat the
reads and calculation, and try again.

### Abort

```text
discard staged updates → untrack → possibly prune history
```

No WAL, MemTable, timestamp, or history update is produced.

### Read-only commit

If `tx.updates.Size() == 0`, there is nothing to validate or persist:

```text
untrack and return success
```

The global commit timestamp does not need to advance for a read-only transaction.

## Nested transactions

Only top-level transactions participate directly in global tracking.

```text
outer KVTX
    └── inner KVTX
```

An inner commit merges its staged updates into the outer transaction. It does not publish
to the database, receive a global timestamp, or append itself to `kv.ongoing`.

When the outer transaction eventually commits:

```text
all accumulated outer updates
        │
        ▼
check against history using the outer transaction's start timestamp
```

Aborting an inner transaction simply discards the inner staged updates; it must not
untrack the still-active outer transaction.

## Relationship to DDIA's lost-update solutions

DDIA describes several approaches. They solve related problems but use different
mechanics.

### Atomic database operation

```sql
UPDATE counters SET value = value + 1 WHERE key = 'counter';
```

The read and write are one database operation, so application code never carries a stale
value between separate steps. This is often preferable when the required change can be
expressed atomically.

### Explicit locking

```text
lock row → read → apply application logic → write → unlock
```

This is pessimistic. It prevents the dangerous interleaving rather than detecting it
afterward.

### Compare-and-set

```text
write new value only if current value still equals previously observed value
```

This validates a particular expected value. The application must detect failure and retry.

### Automatic lost-update detection

```text
run concurrently → detect stale write at commit → abort one transaction
```

This most closely matches 0902. Our engine compares updated keys and commit timestamps
rather than comparing old and current values directly.

## What 0902 prevents

0902 prevents this same-key sequence:

```text
tx1 and tx2 start from the same committed state
tx1 writes key K and commits
tx2 also writes key K
tx2 attempts to commit
                         └──► ErrTXConflict
```

This protects common read-modify-write operations when the transaction writes the same
key whose value its calculation depends on.

## What 0902 does not prevent

### Write skew

Suppose an invariant requires at least one doctor to remain on call:

```text
Initial:
Alice on_call = true
Bob   on_call = true
```

Both transactions read both rows and see two available doctors:

```text
tx1 writes Alice=false
tx2 writes Bob=false
```

The write keys differ:

```text
Alice key != Bob key
```

0902 sees no same-key conflict, so both may commit and violate the invariant. This is
write skew: transactions make decisions from overlapping reads but write disjoint keys.

### Read-write dependency conflicts

0902 tracks keys in `tx.updates`; it does not record every key or range read by a
transaction. If a transaction reads `A` and then writes `B`, a concurrent change to `A`
is not detected merely because `B` depends on it.

### Phantoms and predicate conflicts

A transaction may query a range, observe that no matching row exists, and insert a row
based on that absence. Another transaction can make the same decision and insert a
different physical key. Exact-key history does not represent the searched predicate, so
this conflict is invisible to 0902.

### Full serializability

The result is not guaranteed to be equivalent to running every transaction one at a
time. Preventing write skew and predicate anomalies requires stronger techniques such as
serial execution, two-phase locking, or serializable snapshot isolation.

### Thread safety

The chapter discusses overlapping transaction lifetimes, but it does not yet make
simultaneous goroutine access to `snapshot`, `history`, `ongoing`, the WAL, or the
MemTable safe. Synchronization is addressed in 0903.

## Exact scope of the 0902 implementation

The behavioral changes belong in `kv.go`:

1. Add global transaction metadata to `KV`.
2. Add the starting numeric snapshot to `KVTX`.
3. Extend the transaction target interface so abort can be handled by the target.
4. Track top-level transactions in `NewTX`.
5. Untrack top-level transactions on commit or abort.
6. Check same-key conflicts before touching the WAL.
7. Record successful commit keys with a new timestamp.
8. Prune history according to active transactions.
9. Preserve nested-transaction behavior without globally tracking inner transactions.

The chapter-specific test is `TestTXConflict` in `kv_test.go`. Its central sequence is:

```text
tx1 and tx2 start together
tx1 stages k1=x
tx2 stages k1=y
tx1 commits successfully
tx2 commit returns ErrTXConflict
a later fresh transaction can still update k1 normally
```

## Invariants to preserve during implementation

1. A top-level transaction captures the current numeric snapshot exactly once.
2. Every top-level transaction is tracked until commit or abort finishes.
3. A conflict is checked before WAL or MemTable mutation.
4. Only successful write commits advance `kv.snapshot`.
5. History contains committed updates, never failed attempts.
6. Same-key commits newer than `tx.snapshot` cause `ErrTXConflict`.
7. Different-key updates are not rejected merely because transactions overlap.
8. Finishing a transaction removes it from `kv.ongoing` on every exit path.
9. History needed by an older active transaction is not discarded prematurely.
10. Nested commits remain staged inside the outer transaction until the outer commit.
11. 0901's stable data snapshot and copy-on-write MemTable behavior remain intact.
12. 0805's WAL-before-MemTable durability order remains intact.

## Review questions

1. Why can two counter increments produce only one increment under 0901?
2. What causal relationship exists in a read-modify-update operation?
3. How does optimistic control differ from pessimistic locking?
4. What is the difference between the 0901 data snapshot and `tx.snapshot uint64`?
5. Why does conflict detection compare history timestamps using `>`?
6. Why must conflict detection run before `updateLog`?
7. Why are successful update keys recorded only after the data commit succeeds?
8. Why can two transactions writing different keys both commit?
9. Why does a finished transaction need to be removed even when its commit conflicts?
10. Why can a long-running transaction cause history growth?
11. Why are nested transactions not added independently to `kv.ongoing`?
12. Why does exact-key conflict detection prevent lost updates but not write skew?
13. What should an application do after receiving `ErrTXConflict`?
14. Why is 0902 not yet thread-safe or fully serializable?

## Final mental model

```text
At transaction start:

tx.snapshot = kv.snapshot
tx captures the current 0901 data snapshot
kv.ongoing records tx


While running:

tx reads from [tx.updates + captured snapshot]
tx stages writes privately


At commit:

for every tx update key K:
    if history contains (timestamp > tx.snapshot, same K):
        reject with ErrTXConflict

otherwise:
    commit WAL
    publish copy-on-write MemTable
    increment kv.snapshot
    record updated keys in history

finally:
    remove tx from ongoing
    prune history no active transaction can need
```

The final distinction to remember is:

```text
0901 asks: What committed data may I continue to read?
0902 asks: Has another commit made my intended write stale?
```

