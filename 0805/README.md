# Chapter 0805: Transaction Atomicity

## Goal of this chapter

Chapter 0804 gave us a transaction-shaped interface: writes could be staged in
`KVTX.updates`, reads could see those staged writes, and the caller could invoke
`Commit()` or `Abort()`. Chapter 0805 makes that interface provide a real
all-or-nothing guarantee when a write fails or the process crashes.

The shortest summary is:

```text
0804  collect several changes behind one transaction interface
0805  make those changes survive or disappear as one atomic unit
0901  isolate concurrent transactions from one another
```

The target guarantee is:

```text
Before commit: recovery must ignore every write in the transaction.
After commit:  recovery must restore every write in the transaction.
Never:         recover only some of the transaction's writes.
```

This chapter considers errors and power loss. It does **not** yet solve
concurrent transaction isolation.

## Reading map

Page numbers below use the printed book pagination; a PDF viewer's page number
may differ because of front matter.

### *Build Your Own Database From Scratch in Go*

- Chapter 0805, **“Transaction Atomicity,” pages 90–92**
- Main topics: commit log records, recoverable log offsets, transaction
  rollback, and nested transactions for statement-level atomicity

### Martin Kleppmann, *Designing Data-Intensive Applications*

The most relevant material is in Chapter 7, **“Transactions”**:

- **Pages 215–218:** the meaning of ACID, especially atomicity and durability
- **Pages 219–223:** multi-object transactions and why related objects must stay
  synchronized
- **Pages 221–223:** the distinction between single-object atomic writes and
  transactions that coordinate several objects
- **Page 223:** secondary indexes are separate objects that must be updated with
  their primary records

A second, narrower connection appears in Chapter 9:

- **Pages 343–345:** atomic commit
- **Page 344:** on a single node, data records are written first and a commit
  record is written afterward; recovery treats the commit record as the deciding
  point

Chapter 9 continues into distributed two-phase commit. That part is useful
context, but it is outside 0805: our engine commits to one local log on one node.

## The concept from both books

Kleppmann describes ACID atomicity as abortability: if a fault occurs midway
through several writes, the database discards the partial work. The application
can then retry without first discovering which subset of writes happened.

Chapter 0805 turns that contract into storage-engine mechanics:

```text
Concept                         0805 mechanism
──────────────────────────────  ──────────────────────────────────────
Group several writes            KVTX.updates
Identify a completed group      EntryCommit log record
Make completion durable         fsync in Log.Commit
Ignore an incomplete group      recover only through last commit record
Retry after a write error       reset writer offset to committed offset
Rollback one failed statement   nested KVTX / DBTX
```

Atomicity and durability are related but different:

| Property | Question | 0805 mechanism |
| --- | --- | --- |
| Atomicity | Did all writes happen, or none? | Commit marker and recovery boundary |
| Durability | Will a successful commit survive a crash? | `fsync` before reporting commit success |
| Isolation | Can concurrent transactions observe or overwrite one another? | Not implemented here; begins in 0901 |
| Consistency | Does the transaction preserve application invariants? | The application/DB layer defines valid changes; atomicity helps preserve them |

## Why 0804 is not enough

Suppose changing one indexed row requires these physical KV updates:

```text
1. delete I:(city=LA,id=10)
2. write  P:(id=10) -> city=NY
3. add    I:(city=NY,id=10)
```

`KVTX` can stage the three operations in memory. However, the 0804 commit path
writes them to the log one at a time without a durable marker that says whether
the whole group finished.

Imagine a crash after the second record:

```text
log
────────────────────────────────
DEL I:(LA,10)          written
ADD P:(10)->NY         written
ADD I:(NY,10)          missing  ← power loss
```

If startup replays every valid record, it accepts a half-finished transaction.
The primary row says `NY`, but the `NY` secondary index does not contain it.
Staging alone therefore prevents partial in-memory publication, but it does not
provide crash atomicity.

## The commit record is the decision point

0805 introduces three log operation types:

```go
type EntryOp uint8

const (
    EntryAdd    EntryOp = 0
    EntryDel    EntryOp = 1
    EntryCommit EntryOp = 2
)
```

The old `deleted bool` can distinguish only an add from a delete. An explicit
operation type also allows a record with no user key/value whose meaning is:
“every operation since the previous commit belongs to one completed
transaction.”

A successful log transaction looks like this:

```text
committed prefix                                             new boundary
      │                                                           │
      ▼                                                           ▼
... | ADD primary | DEL old-index | ADD new-index | COMMIT | ...
                                                       ▲
                                                       └─ fsync succeeds
```

The transaction is not committed merely because all data records were written.
The commit record must also be written and synchronized to durable storage.

This gives recovery one unambiguous rule:

```text
Replay operations only through the last valid EntryCommit.
Ignore every valid-looking operation after it.
```

### Crash matrix

| Failure point | Commit marker durable? | Recovery result |
| --- | --- | --- |
| Before the first operation | No | Restore none of the transaction |
| Halfway through operations | No | Restore none of the transaction |
| After all operations, before `EntryCommit` | No | Restore none of the transaction |
| During a partial/corrupt `EntryCommit` | No valid marker | Restore none of the transaction |
| After `EntryCommit` and successful `fsync` | Yes | Restore all operations |

This is the same single-node principle DDIA describes on page 344: the durable
commit record separates an abortable transaction from an irrevocably committed
one.

## Log offsets and rollback

The log now tracks two positions:

```go
type Log struct {
    // ...
    writer struct {
        offset    int64
        committed int64
    }
}
```

Their meanings are different:

```text
committed = end of the last durable committed transaction
offset    = location where the next tentative record will be written
```

During a transaction, `offset` advances while `committed` remains fixed:

```text
                         tentative transaction
                         ┌─────────────────────┐
log: ... committed data | ADD A | DEL B | ADD C
                         ▲                     ▲
                         committed             offset
```

If writing `ADD C` fails, `ResetTX()` restores the logical write position:

```go
func (log *Log) ResetTX() {
    log.writer.offset = log.writer.committed
}
```

The next transaction writes from the last committed boundary and overwrites
the abandoned tail.

### Why `WriteAt` is used

Blind append mode does not respect our resettable logical offset. `WriteAt`
writes exactly where `log.writer.offset` points:

```text
Write succeeds  -> advance offset by the complete encoded record length
Write fails     -> return error without advancing offset
Abort/error     -> reset offset to committed
```

That last rule is crucial. If a partial write incorrectly advances the offset,
the next transaction may begin after corrupted bytes instead of replacing them.

## Commit flow

`Log.Write` no longer calls `fsync` for each individual key. The transaction
writes all operations, then `Log.Commit` writes one commit marker and syncs the
group.

```text
KVTX.Commit()
    │
    ▼
KV.applyTX(tx)
    │
    ├─ KV.updateLog(tx)
    │    ├─ Write ADD/DEL record for every tx.updates entry
    │    ├─ Write EntryCommit
    │    ├─ fsync
    │    └─ committed = offset
    │
    └─ KV.updateMem(tx)
         └─ publish the same changes to the MemTable
```

The log must become durable before the MemTable is updated. If the process dies
after the durable commit but before all MemTable updates, startup reconstructs
the committed state from the log.

`KV.updateLog` uses `defer kv.log.ResetTX()` on both success and failure:

```text
failure: Commit did not advance committed -> offset rolls back
success: Commit advanced committed to offset -> reset changes nothing
```

## Recovery flow

On startup, `Log.Read` uses an `OffsetReader` so it knows the byte position
after each complete record. Whenever it reads `EntryCommit`, that position
becomes the latest committed byte boundary.

At the KV layer, recovery also tracks the number of committed operations:

```text
entries   = every valid ADD/DEL read so far
committed = length of entries at the latest COMMIT record
```

Example log:

```text
ADD A
ADD B
COMMIT       committed = 2
DEL C
ADD D
<torn tail>  end of readable log
```

Before rebuilding `kv.mem`, recovery keeps only:

```text
entries[:committed] = [ADD A, ADD B]
```

`DEL C` and `ADD D` may each be valid records, but their transaction has no
valid commit marker, so they are discarded together.

Checksums and commit markers solve different problems:

| Mechanism | Detects/decides |
| --- | --- |
| CRC checksum | Whether one encoded record is complete and uncorrupted |
| Commit marker | Whether a group of valid records represents a completed transaction |

A checksum alone cannot tell whether three individually valid records were
intended to be a four-record transaction.

## Nested transactions: statement atomicity

One explicit database transaction can contain several SQL statements:

```text
BEGIN outer transaction
    statement 1: succeeds
    statement 2: changes several rows, then fails
    statement 3: may still run
COMMIT outer transaction
```

If statement 2 writes directly into the outer transaction's `updates`, there is
no easy way to remove only statement 2's partial changes while preserving
statement 1. Aborting the entire outer transaction is stronger than necessary,
and keeping the partial statement violates statement atomicity.

0805 solves this with a nested transaction for each public `DBTX` operation:

```text
outer DBTX / KVTX
│
├─ previously successful staged changes
│
└─ inner DBTX / KVTX for one statement
     ├─ success -> merge inner.updates into outer.updates
     └─ failure -> discard inner transaction only
```

### Mock execution

Assume the outer transaction already contains:

```text
outer.updates: A -> 10
```

An update statement creates an inner transaction and stages:

```text
inner.updates:
    B -> 20
    C -> 30
```

If the statement succeeds:

```text
inner.Commit()
    -> outer.applyTX(inner)
    -> merge B and C into outer.updates

outer.updates:
    A -> 10
    B -> 20
    C -> 30
```

No disk commit occurs yet. Only the eventual outermost commit writes to the
real `KV` log.

If the statement fails after staging `B` but before `C`:

```text
inner.Abort()
    -> discard inner

outer.updates:
    A -> 10       unchanged
```

## One `Commit` method, two targets

In 0804, every `KVTX` targeted the real store:

```go
target *KV
```

In 0805, a transaction may target either the real `KV` or an outer `KVTX`:

```go
target interface {
    applyTX(*KVTX) error
}
```

Both targets implement the same operation but with different meanings:

```text
inner KVTX.Commit()
    target = outer *KVTX
    action = merge staged changes into outer transaction

outer KVTX.Commit()
    target = *KV
    action = commit to log, fsync, then update MemTable
```

This is a compact form of polymorphism: `KVTX.Commit()` does not need an
`if nested` branch. It delegates to whichever target created it.

### Nested read view

An inner transaction must see:

```text
1. its own updates
2. the outer transaction's updates
3. committed MemTable data
4. SSTables
```

Conceptually:

```text
inner.levels
┌─────────────────────────┐
│ inner.updates           │ newest
├─────────────────────────┤
│ outer.updates           │
├─────────────────────────┤
│ kv.mem                  │
├─────────────────────────┤
│ kv.main[0], [1], ...    │ oldest
└─────────────────────────┘
```

That ordering preserves read-your-writes at both nesting levels.

## DBTX wrappers and internal helpers

Public statement methods create a nested transaction:

```text
DBTX.Update
    -> inner := outer.NewTX()
    -> inner.update(...)       internal implementation
    -> commit inner on success, abort inner on failure
```

The lower-case helpers matter:

```text
update, delete, execStmt    perform work in the current transaction
Update, Delete, ExecStmt    create a nested statement boundary
```

Without that split, a helper calling another public method could repeatedly
create unnecessary nested transactions or commit at the wrong boundary.

The non-transactional `DB` wrappers still create one outer transaction and call
the internal helper directly:

```text
db.Update(...)
    -> db.NewTX()             outermost transaction
    -> tx.update(...)
    -> commit to KV on success
```

## End-to-end indexed update

For `city: LA -> NY`, the complete 0805 path is:

```text
outer DBTX.Update(row)
    │
    ├─ create inner DBTX
    │
    ├─ inner reads old primary row
    ├─ inner stages deletion of old index
    ├─ inner stages replacement primary row
    ├─ inner stages insertion of new index
    │
    ├─ statement succeeds
    │    └─ merge inner updates into outer KVTX
    │
    └─ outer Commit
         ├─ write DEL old-index
         ├─ write ADD primary
         ├─ write ADD new-index
         ├─ write COMMIT
         ├─ fsync
         └─ publish all changes to kv.mem
```

After a crash, recovery produces exactly one of these states:

```text
Before transaction             After transaction
────────────────────────       ────────────────────────
P:(10) -> city=LA              P:(10) -> city=NY
I:(LA,10) exists               I:(LA,10) missing
I:(NY,10) missing              I:(NY,10) exists
```

It must never reconstruct a mixture of the two columns.

## Files involved

| File | Main 0805 responsibility |
| --- | --- |
| `kv_entry.go` | Replace the delete flag with `EntryOp`; encode/decode `EntryCommit`. |
| `log.go` | Track read/write offsets, write with `WriteAt`, commit with `fsync`, and reset failed transactions. |
| `kv.go` | Write transaction commit markers, recover only committed operations, and implement nested `KVTX`. |
| `table.go` | Implement nested `DBTX` and statement-level wrappers. |
| `kv_test.go` | Verify transaction recovery when the log is truncated or corrupted before commit. |
| `table_test.go` | Continue verifying row/index behavior through the DB transaction interface. |

The current `0805` directory was copied from 0804 and still uses the
`db0804` package declaration. Before implementation, package declarations and
tests should be aligned to `db0805`, matching `db_solution/0805`.

## Recommended implementation order

Work from the on-disk format upward:

1. Add `EntryOp`, including `EntryCommit`, in `kv_entry.go`.
2. Update entry encoding and decoding.
3. Add reader/writer offsets to `Log`.
4. Change `Log.Write` to use the tracked `WriteAt` position.
5. Add `Log.Commit()` and `Log.ResetTX()`.
6. Change `KV.updateLog()` to write ADD/DEL operations and one final commit
   marker.
7. Change startup recovery to apply only operations through the last commit.
8. Generalize `KVTX.target` and implement nested `KVTX.NewTX/applyTX`.
9. Add `DBTX.NewTX()` and wrap public statement APIs in inner transactions.
10. Run focused recovery tests, then the complete package test suite.

This sequence keeps each step understandable: first define the durable format,
then make recovery honor it, and only then add nested in-memory boundaries.

## Invariants to protect

Use these as a review checklist:

- A failed `Log.Write` does not advance the writer offset.
- Only `Log.Commit` advances the committed offset.
- `fsync` succeeds before commit is reported as successful.
- `KV.updateMem` runs only after the log transaction commits.
- Startup ignores all operations after the last valid commit record.
- An inner transaction sees both its own and its outer transaction's updates.
- Inner commit changes only the outer write set; it does not touch disk.
- Inner abort leaves the outer write set unchanged.
- Outermost commit is the only path that publishes to the real `KV`.
- Primary rows and every secondary-index entry share one commit boundary.

## Tests worth understanding

### Recovery from a torn transaction

Prepare one previously committed key, then commit several keys in one new
transaction. Corrupt or truncate the end of that transaction's log record or
commit marker. After reopening:

```text
old committed key       present
every key from bad TX   absent
```

The important assertion is not merely that the last key is absent. **Every key
from the incomplete transaction must be absent.**

### Nested statement failure

A useful mental or additional test is:

```text
outer.Set(A)
inner.Set(B)
inner.Set(C) -> simulated error
inner.Abort()

outer.Get(A) -> present
outer.Get(B) -> absent
outer.Get(C) -> absent
```

### Successful nested merge

```text
outer.Set(A)
inner.Set(B)
inner.Commit()

outer sees A and B
base KV sees neither before outer commit
base KV sees both after outer commit
```

## Common misunderstandings

### “Every log record has a checksum, so the transaction is atomic.”

No. A checksum establishes record integrity. It does not say whether a sequence
of valid records was complete. The commit marker establishes transaction
completeness.

### “Writing all data records means the transaction committed.”

No. Until the commit marker is durable, recovery must treat the writes as
abortable.

### “Resetting the offset erases the bytes immediately.”

Not necessarily. It changes the logical next-write position. Future writes
overwrite the abandoned tail, and recovery ignores anything beyond the last
committed boundary.

### “Nested commit writes to disk.”

Only the outermost commit targets `KV`. An inner commit targets another
`KVTX`, so it merges write sets in memory.

### “Atomicity prevents concurrent lost updates.”

No. Atomicity handles partial failure. Lost updates and visibility between
concurrent transactions are isolation problems addressed by later chapters.

## Final mental model

```text
DBTX gives statements an all-or-nothing boundary.
KVTX holds the private set of physical changes.
Nested KVTX protects one statement inside a larger transaction.
EntryCommit turns several WAL records into one recoverable unit.
fsync makes that committed unit durable.
Recovery trusts only the last valid commit boundary.
```

0804 taught the engine to *collect* related writes. 0805 teaches it to make a
durable decision about the entire collection. That is the difference between
having transaction-shaped methods and actually providing transaction
atomicity.
