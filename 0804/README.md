# Chapter 0804: Transaction Interface

## The idea in one sentence

0804 introduces a transaction as a private workspace for a group of KV and DB
operations: reads can see the transaction's pending writes, while the real
database is changed only when `Commit()` is called.

The motivating example is an indexed row. One logical row can produce several
physical KV entries—a primary entry plus one entry for each secondary index.
Those entries must be treated as one logical operation. Otherwise a failure in
the middle of an update can leave the table row and its indexes disagreeing.

```go
tx := kv.NewTX()
tx.Set([]byte("k1"), []byte("v1"))
tx.Set([]byte("k2"), []byte("v2"))
err := tx.Commit()
```

The interface is added in this chapter; complete crash atomicity and concurrent
transaction isolation are deliberately later responsibilities.

## What “transaction” means here

A transaction groups reads and writes into one logical unit. The application
should be able to perform several operations and then choose one of two
outcomes:

```text
commit  -> make the staged updates visible and durable
abort   -> discard the staged updates
```

This gives the upper layers a stable unit of work. For example, `DBTX.Update`
can remove old index entries and create new primary/secondary entries without
publishing each intermediate state to ordinary reads.

There are two meanings of “transaction processing” that are easy to mix up:

* In 0804, a transaction is an API and correctness boundary around a set of
  reads and writes.
* In DDIA pp. 87–101, OLTP/transaction processing describes a workload: many
  interactive, low-latency requests that usually read or modify a small number
  of records by key. It is contrasted with OLAP, where fewer but much larger
  analytical scans aggregate over historical data.

The concepts are related, but they are not identical. An OLTP request often
uses a transaction, but “OLTP” by itself does not promise ACID. Likewise, a
transaction API can be used for work that is not a commercial sale or even for
more than one statement.

## Why this is needed in this database

Before secondary indexes, one row mostly corresponded to one primary KV entry.
With indexes, one logical operation has multiple physical effects.

For a table such as:

```sql
CREATE TABLE users (
    id INT64,
    city STRING,
    name STRING,
    INDEX (city),
    PRIMARY KEY (id)
);
```

the row `(10, "LA", "Morgan")` creates entries conceptually like:

```text
primary index:    (id=10)             -> city/name value
city index:       (city="LA", id=10) -> empty value
```

Updating the city requires at least:

```text
delete old secondary key  (LA, 10)
write the new primary row  (10) with city=NY
write new secondary key    (NY, 10)
```

If those writes are exposed one at a time, a reader could temporarily find a
row through the old index, fail to find it through the new index, or see an
index entry whose primary row does not yet match it. A transaction gives the DB
layer one context in which all of these changes can be assembled.

## The design: staged updates plus merged reads

`KVTX` contains the target database and two transaction-local structures:

```go
type KVTX struct {
    target  *KV
    updates SortedArray
    levels  MergedSortedKV
}
```

### 1. `updates` is the private write set

`KVTX.SetEx` does not immediately modify `KV.mem` and does not immediately
append a log record. It records the newest value in `tx.updates`. Deletes are
also represented there as tombstones.

This is the essential separation:

```text
transaction-local state:  tx.updates
committed in-memory state: kv.mem
on-disk history:           kv.log
```

The write set is sorted because the existing storage engine already has sorted
arrays and merged iterators. Staging therefore fits the LSM-style architecture
instead of introducing a second lookup mechanism.

### 2. `levels` gives reads the right view

A transaction must read its own earlier writes:

```go
tx.Set([]byte("x"), []byte("new"))
value, ok, _ := tx.Get([]byte("x")) // must return "new"
```

`NewTX` creates a merged view with this priority:

```text
1. tx.updates  // newest, transaction-local changes
2. kv.mem      // committed recent changes
3. kv.main     // immutable sorted files / older levels
```

`tx.Seek` reads through `MergedSortedKV`, then filters tombstones. In effect,
the transaction write set behaves like one additional, highest-priority LSM
level. This is the key implementation insight: read-your-writes can be added
by composing the existing sorted lookup layers rather than copying the whole
database.

The same view is important for multiple operations in one transaction. A later
`SetEx`, `Del`, index lookup, or row update must reason about the state produced
by earlier operations in that same transaction.

### 3. `Commit` applies the staged work

The intended commit flow in this chapter is:

```text
tx.Commit()
    ├─ write every entry in tx.updates to kv.log
    └─ apply every entry in tx.updates to kv.mem
```

The code delegates this to `KV.applyTX`:

```go
func (kv *KV) applyTX(tx *KVTX) error {
    if err := kv.updateLog(tx); err != nil {
        return err
    }
    kv.updateMem(tx)
    return nil
}
```

Writing the log before updating memory preserves the existing durability
ordering: the log is the recovery source, and `mem` is the live lookup state.
The transaction refactor changes *when* entries are collected, while retaining
the existing log-plus-memory architecture.

### 4. `Abort` discards the workspace

At this stage `Abort()` has an empty body. That is safe for the current design
because pending writes exist only in `tx.updates`; they have not changed
`kv.mem` or the log. Aborting means allowing the transaction object and its
write set to be discarded.

This is a useful example of an interface arriving before its final machinery.
The method exists so callers can write correct transaction-shaped code now,
while later chapters can add stronger rollback behavior for failures during
commit.

## KV API and DB API

The low-level API is the foundation:

```go
tx := kv.NewTX()
defer tx.Abort()

_, err := tx.SetEx(key, value, ModeUpsert)
if err != nil {
    return err
}
return tx.Commit()
```

The old one-operation `KV` methods remain available as convenience wrappers.
For example, `KV.Get` creates a transaction, reads through it, and aborts it;
`KV.SetEx` creates a transaction, performs one staged update, then commits or
aborts through `abortOrCommit`.

The relational layer follows the same pattern:

```go
type DBTX struct {
    kv     *KVTX
    tables map[string]Schema
}
```

`DB.NewTX` wraps `KV.NewTX`. `DBTX.Insert`, `Select`, `Update`, `Delete`,
`Seek`, `Range`, and `ExecStmt` operate through that shared transaction. The
non-transactional `DB` methods remain convenient one-operation calls that
create a `DBTX` internally.

This layering matters:

```text
DBTX operation
    -> several primary/index KV operations
        -> one KVTX write set
            -> one commit boundary
```

A single SQL statement can therefore be implemented as multiple lower-level
updates without making the caller manage every physical index entry.

## How a row update works inside `DBTX`

The reference implementation follows this shape:

1. Encode the row's primary key and value.
2. Read the existing row through `tx.kv`, so a prior write in this transaction
   is visible.
3. Check the requested mode (`Insert`, `Update`, or `Upsert`).
4. If an old row exists, delete its primary and secondary index entries into the
   same transaction write set.
5. Add the new primary entry and all new secondary entries to that write set.
6. Commit only after the DB operation or statement has succeeded.

Conceptually:

```text
DBTX.Update(row)
    ├─ tx.kv.Get(primary key)
    ├─ tx.Delete(old row)
    │    ├─ tx.kv.Del(old primary key)
    │    └─ tx.kv.Del(old secondary keys)
    ├─ tx.kv.SetEx(new primary key, new value)
    └─ tx.kv.SetEx(new secondary keys, nil)
          ... later: one tx.Commit()
```

The important word is “staged.” The intermediate delete and inserts are
visible to this transaction's merged read view, but are not yet committed to
the database's live state.

## What this chapter guarantees—and what it does not

### Guaranteed by the interface/design

* A transaction can contain multiple KV operations.
* Reads in the transaction see its own pending updates.
* Deletes hide entries through tombstones in the transaction view.
* The DB layer can update a row and all of its indexes through one transaction
  object.
* Existing single-operation `KV` and `DB` APIs continue to work as wrappers.
* Transaction code has an explicit commit/abort lifecycle.

### Not fully guaranteed yet

* **Crash atomicity of the entire transaction:** the current commit writes
  multiple log entries individually. A crash between entries can leave a
  partially logged transaction. 0805 adds transaction framing/rollback logic.
* **Concurrent transaction isolation:** there is no locking, snapshot
  isolation, or conflict detection here. Later concurrency work decides what a
  transaction may observe from other transactions.
* **Nested statement transactions:** a DB transaction may eventually contain
  statements that each need their own rollback boundary. That is addressed in
  the following atomicity work.
* **General ACID by merely having `NewTX`:** the presence of a transaction
  interface is not proof that all ACID properties are implemented.

This boundary is especially important when reading DDIA: the book separates
the OLTP workload label from ACID guarantees and discusses ACID transactions in
its later transactions chapter.

## Connection to DDIA pp. 87–101

The useful connection is architectural:

| DDIA observation | Consequence for 0804 |
| --- | --- |
| OLTP serves interactive users with low-latency reads/writes | Point lookups and small row updates must remain cheap. |
| OLTP commonly fetches records through indexes | A row update may touch primary and secondary index keys. |
| OLAP scans many records and computes aggregates | This project’s sorted/indexed KV path is primarily an OLTP-style path, not a columnar warehouse engine. |
| Row-oriented/index-oriented engines favor current state | The transaction view must provide a coherent current state while writes are staged. |
| LSM-style storage turns writes into in-memory changes plus later durable files | `tx.updates` naturally acts as a new in-memory sorted level before commit. |
| Column stores and materialized aggregates optimize read-heavy analytics but make writes more involved | It reinforces why this chapter focuses on small coordinated writes, while analytics would need different physical structures. |

So the books meet at the boundary between application behavior and storage
design: the workload determines what operations need to be fast, and the
storage layout determines how those operations are coordinated. 0804 applies
that reasoning to the OLTP side by introducing a transaction context over the
existing sorted KV engine.

## The closest DDIA chapter: Chapter 7, “Transactions”

If the goal is to understand the transaction interface in 0804, DDIA Chapter 7
is the important reading—not pp. 87–101. The most relevant sections are:

| DDIA section | Why it maps to 0804 |
| --- | --- |
| pp. 213–215, introduction and “The slippery concept of a transaction” | Defines a transaction as a group of reads/writes with an all-or-nothing `commit` or `abort` outcome. This is the conceptual basis for `NewTX`, `Commit`, and `Abort`. |
| pp. 215–218, ACID | Separates atomicity, consistency, isolation, and durability. It prevents us from calling the 0804 interface fully ACID before later chapters implement crash atomicity and isolation. |
| pp. 219–221, single-object and multi-object operations | Explains why several writes must be coordinated and how logs help with crash recovery. This is directly relevant to a row update producing multiple KV entries. |
| pp. 221–223, “The need for multi-object transactions” and “Handling errors and aborts” | The closest match: it explicitly discusses secondary indexes as separate objects that can become inconsistent without a transaction, and explains abort/retry as the error-handling model. |
| pp. 224 onward, weak isolation levels | Describes the concurrency work that 0804 intentionally postpones: read committed, snapshot isolation, lost updates, write skew, and serializability. |

The most important correspondence is:

```text
DDIA:      group writes to multiple objects; commit all or abort all
0804:      stage primary/index KV entries in tx.updates; Commit applies them

DDIA:      secondary indexes are separate objects that must stay in sync
0804:      DBTX stages deletion of old index keys and insertion of new keys

DDIA:      abort should discard partial work and permit safe retry
0804:      staged writes make Abort cheap now; durable rollback is deferred
```

There is also an important implementation gap. DDIA uses “atomicity” to mean
that if a fault occurs halfway through a transaction, all prior writes are
discarded. In 0804, `Abort()` discards uncommitted in-memory staging, but the
current `Commit()` writes multiple log entries one by one. A crash during that
sequence could still leave a partial transaction in the log. That is why the
0804 text correctly treats this as an interface/refactoring step and leaves
full transaction atomicity to 0805.

For review, read 0804 alongside DDIA Chapter 7 in this order:

1. DDIA pp. 213–215: what problem transactions solve.
2. DDIA pp. 215–218: what each ACID word actually means.
3. DDIA pp. 219–223: why multi-object/index updates need transactions.
4. 0804: how a sorted transaction overlay implements the first useful part of
   that model.
5. DDIA pp. 224–253: what additional machinery is needed for concurrent
   transactions.

## A concrete mental model

Think of `KVTX` as a private overlay, not as a copy of the database:

```text
committed database:       a=old, b=old
transaction overlay:     a=new, c=created, b=deleted

transaction reads:       a=new, c=created, b=missing
other database readers:  a=old, b=old, c=missing

after Commit:             a=new, b=missing, c=created
after Abort:              a=old, b=old, c=missing
```

The overlay is small and sorted; the base database remains available beneath
it. This is why the design is efficient enough for the current LSM-style
engine and why read-your-writes works naturally.

## Review checklist

When reviewing this chapter, be able to answer:

1. Why does adding a secondary index turn one row update into a multi-key
   operation?
2. Why must `tx.updates` have higher read priority than `kv.mem` and SSTables?
3. What happens when a key is deleted in the transaction but still exists in a
   lower storage level?
4. Why can the old `KV.Set` API be implemented as “create transaction, stage,
   commit”?
5. Why is an empty `Abort()` sufficient for staged writes in 0804?
6. Why does the current `Commit()` still fail to provide crash atomicity for a
   multi-entry transaction?
7. What is the difference between the OLTP workload discussed by DDIA and the
   ACID transaction abstraction implemented here?
8. Why would the techniques described by DDIA for column-oriented OLAP storage
   not replace this transaction interface?

## Source notes

* *Database Internals in 45 Steps (Go)*, `0804: Transaction Interface`, pp.
  87–89: transaction API, staged updates, read-your-writes, commit/abort, and
  DB transaction wrapping.
* Martin Kleppmann, *Designing Data-Intensive Applications*, pp. 87–101:
  OLTP versus OLAP, data warehousing, star schemas, column-oriented storage,
  compression, sorted column storage, LSM-backed writes, and materialized
  aggregates. The detailed ACID transaction discussion is later in the book.
