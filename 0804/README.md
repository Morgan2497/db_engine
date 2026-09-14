# Chapter 0804: Transaction Interface

## Goal of this chapter

0804 introduces a transaction as a private workspace for a group of KV and DB
operations. Writes are staged in that workspace, reads see those staged writes,
and the caller eventually chooses `Commit()` or `Abort()`.

The shortest accurate summary is:

```text
0804  transaction interface + staging + read-your-writes
0805  crash-safe all-or-nothing transaction
09xx  isolation between concurrent transactions
```

0804 is therefore a structural step. It creates the API and data flow required
for atomic transactions, but it does not yet provide every ACID guarantee.

## Why transactions become necessary now

Before secondary indexes, one logical row mostly corresponded to one primary
KV entry. After indexes are added, one row is represented by several physical
entries.

Consider:

```sql
CREATE TABLE users (
    id INT64,
    city STRING,
    name STRING,
    INDEX (city),
    PRIMARY KEY (id)
);
```

The row `(10, "LA", "Morgan")` is stored conceptually as:

```text
primary entry:    P:(id=10)       -> {city=LA, name=Morgan}
secondary entry:  I:(city=LA,10)  -> empty
```

Changing the city from `LA` to `NY` requires several KV changes:

```text
1. delete I:(city=LA,10)
2. update P:(id=10) -> {city=NY, name=Morgan}
3. insert I:(city=NY,10)
```

These are three physical changes but only one logical row update.

### Before 0804: every change is immediate

```text
DB.Update(row)
    │
    ├─ delete old index ──> append log ──> update mem
    │
    ├─ update primary ────> append log ──> update mem
    │
    └─ insert new index ──> append log ──> update mem
```

If an error occurs after the first operation, the database may be left in an
inconsistent state:

```text
primary entry:    P:(10) -> city=LA
old city index:   missing
new city index:   missing
```

The DB needs somewhere to assemble all related changes before applying them to
the base KV store. That workspace is `KVTX`.

## What 0804 adds

```text
Application / SQL
        │
        ▼
┌──────────────────────────────┐
│ DBTX                         │
│ rows, schemas and indexes    │
│ Insert/Update/Delete/Select  │
└──────────────┬───────────────┘
               │ produces physical key/value changes
               ▼
┌──────────────────────────────┐
│ KVTX                         │
│ private sorted write set     │
│ Set/Get/Del/Seek             │
└──────────────┬───────────────┘
               │ Commit
               ▼
┌──────────────────────────────┐
│ KV                           │
│ log + MemTable + SSTables    │
└──────────────────────────────┘
```

The two transaction types have different responsibilities:

| Type | Understands | Responsibility |
| --- | --- | --- |
| `DBTX` | rows, schemas, primary indexes and secondary indexes | Turn one logical DB operation into all required KV operations. |
| `KVTX` | byte keys, byte values and tombstones | Stage physical changes and provide a transaction-local read view. |
| `KV` | log, MemTable and SSTables | Store committed data and recover it from disk. |

The central flow is:

```text
one DBTX operation
    -> several physical KV operations
        -> one KVTX write set
            -> one commit boundary
```

## Visual walkthrough: updating `LA` to `NY`

At the beginning, only the committed database exists:

```text
Committed database
──────────────────────────────────────
P:(10)       -> {city=LA, name=Morgan}
I:(LA,10)    -> exists
```

Create a transaction and update the row:

```go
tx := db.NewTX()
updated, err := tx.Update(schema, newRow)
```

The operation builds a private overlay:

```text
Committed database                 tx.updates
────────────────────────           ─────────────────────────────
P:(10) -> city=LA                  P:(10)     -> city=NY
I:(LA,10) exists                   I:(LA,10)  -> TOMBSTONE
                                   I:(NY,10)  -> exists
```

While the transaction is being assembled:

```text
base KV state              unchanged
transaction's own view     reflects the pending NY update
```

The transaction then has two intended outcomes:

```text
                         ┌─ Commit ─> append updates to log
                         │            then apply them to kv.mem
tx.updates ──────────────┤
                         └─ Abort ──> abandon the transaction object
```

After a successful commit:

```text
Committed database
──────────────────────────────────────
P:(10)       -> {city=NY, name=Morgan}
I:(LA,10)    -> missing
I:(NY,10)    -> exists
```

After aborting and discarding the transaction:

```text
Committed database
──────────────────────────────────────
P:(10)       -> {city=LA, name=Morgan}
I:(LA,10)    -> exists
I:(NY,10)    -> missing
```

This diagram describes staging and the intended final outcomes. It does not
claim that 0804 already provides crash atomicity or isolation from concurrent
transactions.

## `KVTX`: the private overlay

```go
type KVTX struct {
    target  *KV
    updates SortedArray
    levels  MergedSortedKV
}
```

### `target`

`target` points to the real KV store. It is needed when the transaction commits:

```text
tx.target.log
tx.target.mem
tx.target.main
```

### `updates`

`updates` is the transaction's private write set. `KVTX.SetEx` and `KVTX.Del`
change this sorted array rather than immediately changing `kv.mem` or the log.

```text
transaction-local:  tx.updates
committed memory:   kv.mem
durable history:    kv.log
older data:         kv.main / SSTables
```

Deletes are stored as tombstones. Multiple writes to the same key are reduced
to that key's latest transaction-local state.

### `levels`

`levels` combines the transaction overlay with the existing LSM-tree levels:

```text
Transaction read
      │
      ▼
┌──────────────────────────┐
│ 1. tx.updates            │ newest; private pending changes
├──────────────────────────┤
│ 2. kv.mem                │ recent committed changes
├──────────────────────────┤
│ 3. kv.main[0]            │ SSTable
├──────────────────────────┤
│ 4. kv.main[1] ...        │ older SSTables
└──────────────────────────┘
```

The first matching entry wins. This makes `tx.updates` act like one additional,
highest-priority LSM level.

## Read-your-writes

A transaction must see changes it made earlier:

```go
tx.Set([]byte("x"), []byte("new"))
value, ok, _ := tx.Get([]byte("x")) // value must be "new"
```

Suppose the levels contain:

```text
tx.updates:  x -> new
kv.mem:      x -> old
SSTable:     x -> older
```

The transaction reads `new` because `tx.updates` has the highest priority.

Deletes use the same rule:

```text
tx.updates:  x -> TOMBSTONE
kv.mem:      x -> old
```

The tombstone hides the lower value, so the transaction sees `x` as missing.
`KVTX.Seek` obtains a merged iterator and `filterDeleted` prevents tombstones
from appearing as live records.

Read-your-writes is important for more than user-facing reads. A later
`SetEx`, `Del`, index lookup, or row update in the same transaction must make
its decision using the state produced by earlier operations in that
transaction.

## Transaction lifecycle

```text
NewTX
  │
  ├─ Set / Del ──> stage entries in tx.updates
  │
  ├─ Get / Seek ─> read updates + mem + SSTables
  │
  ├─ Commit ─────> write log entries, then update kv.mem
  │
  └─ Abort ──────> stop using and discard the transaction object
```

### `NewTX`

`NewTX` creates an empty write set and assembles the merged read view:

```go
func (kv *KV) NewTX() *KVTX {
    tx := &KVTX{target: kv}
    tx.levels = MergedSortedKV{&tx.updates, &kv.mem}
    for i := range kv.main {
        tx.levels = append(tx.levels, &kv.main[i])
    }
    return tx
}
```

It does not copy the database. The transaction stores only its own changes and
reads unchanged data from the underlying levels.

### `Commit`

```text
tx.Commit()
    │
    ▼
KV.applyTX(tx)
    │
    ├─ updateLog(tx) ──> append every staged entry to kv.log
    │
    └─ updateMem(tx) ──> apply every staged entry to kv.mem
```

The implementation is:

```go
func (kv *KV) applyTX(tx *KVTX) error {
    if err := kv.updateLog(tx); err != nil {
        return err
    }
    kv.updateMem(tx)
    return nil
}
```

The log is written before memory because the log is the recovery source. The
live MemTable is changed only after logging succeeds.

However, the entries are still logged individually. If a crash happens halfway
through `updateLog`, recovery may find only part of the transaction. 0805 adds
the transaction framing and rollback needed to distinguish a complete commit
from a partial one.

### `Abort`

```go
func (tx *KVTX) Abort() {}
```

Before commit, writes exist only in `tx.updates`, so there is nothing in
`kv.mem` or `kv.log` to undo. Aborting currently means stopping use of the
transaction and allowing the whole object to be discarded.

This is only a caller-enforced convention. `Abort()` does not:

* clear `tx.updates`;
* mark the transaction closed;
* prevent later reads or writes through the object;
* prevent a later call to `Commit()`.

The caller must not reuse a transaction after aborting it.

## How `DBTX.Update` uses `KVTX`

A logical row update follows this shape:

```text
DBTX.Update(new row)
    │
    ├─ tx.kv.Get(primary key)       read old row through TX view
    │
    ├─ tx.Delete(old row)
    │    ├─ tx.kv.Del(primary key)
    │    └─ tx.kv.Del(old secondary keys)
    │
    ├─ tx.kv.SetEx(new primary entry)
    └─ tx.kv.SetEx(new secondary entries)

                       all changes land in tx.updates
                                      │
                                      ▼
                                 tx.Commit()
```

The delete and inserts are visible to later operations in this transaction,
but they do not change the base KV state while the update is being assembled.

`DBTX.Insert`, `Upsert`, `Delete`, `Select`, `Seek`, `Range`, and `ExecStmt`
follow the same principle: all lower-level work uses one shared `KVTX`.

## Compatibility with the old API

The original one-operation `KV` and `DB` methods remain available. They are now
convenience wrappers around transactions:

```text
kv.Set(key, value)
    │
    ├─ tx := kv.NewTX()
    ├─ tx.Set(key, value)
    └─ tx.Commit() or tx.Abort()
```

The same pattern applies to `DB.Insert`, `DB.Update`, and `DB.Delete`.

This provides two API styles:

```go
// One operation and one implicit transaction.
db.Insert(schema, row)

// Several operations sharing one explicit transaction.
tx := db.NewTX()
defer tx.Abort()

tx.Insert(schema, row1)
tx.Update(schema, row2)
err := tx.Commit()
```

## What 0804 provides—and what it postpones

### Provided in 0804

* A transaction object can collect multiple KV operations.
* Writes and tombstones are staged in a private sorted overlay.
* Reads in a transaction see its own pending changes.
* Before `Commit()` begins, staging has not changed `kv.log` or `kv.mem`.
* A DB operation can send all primary and secondary index changes through one
  `KVTX`.
* Existing one-operation APIs continue to work through implicit transactions.
* The API establishes a `NewTX` → operations → `Commit`/`Abort` convention.

### Not yet guaranteed

* **Crash atomicity:** log entries are written one at a time. A crash can leave
  a partial transaction in the log. This is the focus of 0805.
* **Atomic visibility during commit:** `updateMem` applies entries one at a
  time. 0804 has no concurrency control preventing another reader from
  observing that application in progress.
* **Isolation:** there are no locks, snapshots, transaction versions, or
  conflict detection. These arrive in the concurrency chapters.
* **Lifecycle enforcement:** `Commit()` and `Abort()` do not close the object
  or reject further use.
* **Nested rollback boundaries:** a statement inside a larger DB transaction
  may eventually need its own rollback scope. That comes with the following
  atomicity work.

The existence of `NewTX`, `Commit`, and `Abort` does not by itself mean that
the database is fully ACID. 0804 creates the transaction-shaped programming
model on which those guarantees can be built.

## Connection to *Designing Data-Intensive Applications*

### Direct companion: Chapter 7, “Transactions”

DDIA Chapter 7 is the closest conceptual match:

| DDIA section | Connection to 0804 |
| --- | --- |
| pp. 213–215, introduction | Defines a transaction as a group of reads and writes with `commit` or `abort` as the final outcome. |
| pp. 215–218, ACID | Separates atomicity, consistency, isolation, and durability, showing why a transaction API alone is not full ACID. |
| pp. 219–221, single-object and multi-object operations | Explains why several writes sometimes need one all-or-nothing boundary. |
| pp. 221–223, multi-object transactions and aborts | Explicitly identifies secondary indexes as separate objects that can become inconsistent, matching 0804's motivation. |
| pp. 224–253, isolation levels | Describes concurrency problems and guarantees that 0804 intentionally postpones. |

The central correspondence is:

```text
DDIA                         0804
───────────────────────────  ─────────────────────────────────────
group related operations     collect entries in tx.updates
secondary indexes must sync  route all index changes through DBTX
read pending own writes      put tx.updates above the base LSM tree
commit or abort              expose Commit() and Abort()
atomicity required           interface now; crash atomicity in 0805
```

The most useful reading order is:

1. DDIA pp. 213–215: what problem transactions solve.
2. DDIA pp. 215–218: what each ACID term means.
3. DDIA pp. 219–223: why multi-object and index updates need transactions.
4. 0804: how this engine builds a transaction-local sorted overlay.
5. DDIA pp. 224–253: what concurrent transactions additionally require.

### Supporting background: DDIA pp. 87–101

Pages 87–101 discuss OLTP versus OLAP rather than the transaction interface.
They still provide useful workload context:

| Term | Meaning | Connection to this project |
| --- | --- | --- |
| OLTP | Many low-latency operations on a small number of records, usually found by key or index. | This engine's indexed row operations follow an OLTP-style access pattern. |
| OLAP | Large scans and aggregates over historical data. | Columnar storage and materialized aggregates address a different problem. |
| Transaction | A correctness/programming abstraction that groups operations. | `DBTX` and `KVTX` begin implementing this abstraction. |
| ACID | A collection of transaction guarantees. | Only part of the machinery exists in 0804. |

OLTP is a workload description; it does not automatically imply ACID.

## Review checklist

You understand this chapter if you can answer these questions:

1. Why does one indexed row produce several physical KV entries?
2. What inconsistent states can occur if those entries are updated immediately?
3. Why does `KVTX` store changes in `tx.updates` instead of `kv.mem`?
4. Why is `tx.updates` the highest-priority merged LSM level?
5. How does a tombstone hide an older value from transaction reads?
6. What is the difference between `DBTX` and `KVTX`?
7. Why does commit write the log before updating memory?
8. Why is an empty `Abort()` sufficient only if the transaction is discarded?
9. Why is 0804 not yet crash-atomic or isolated?
10. What does 0805 need to add on top of this interface?
11. How is the OLTP workload different from an ACID transaction guarantee?

## Source notes

* *Database Internals in 45 Steps (Go)*, `0804: Transaction Interface`, pp.
  87–89: transaction API, staged updates, read-your-writes, commit/abort, and
  DB transaction wrapping.
* Martin Kleppmann, *Designing Data-Intensive Applications*, Chapter 7,
  especially pp. 213–223: transaction boundaries, ACID, multi-object updates,
  secondary-index consistency, commit, abort, and retry; pp. 224–253 continue
  into isolation and concurrency anomalies.
* Martin Kleppmann, *Designing Data-Intensive Applications*, pp. 87–101:
  OLTP versus OLAP, data warehousing, column-oriented storage, LSM-backed
  writes, and materialized aggregates.
