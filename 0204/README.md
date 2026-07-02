# Chapter 0204: CRUD (The Relational DB Wrapper)

## Overview
This chapter represents a major structural shift in our puzzle-piece architecture. We are introducing the `DB` struct, which wraps our underlying `KV` storage engine. This is the exact layer where we stop interacting primarily with raw byte slices and begin operating on structured relational primitives: `Schema` and `Row` objects. 

By wrapping the `KV` store, we are mapping high-level SQL CRUD (Create, Read, Update, Delete) operations directly to our fundamental KV `Get`, `SetEx`, and `Del` mechanisms.

## Core Implementation: The DB Struct & API

We introduce the top-level `DB` struct and its primary-key-based data access API. 

```go
type DB struct {
	KV KV
}

func (db *DB) Open() error  { return db.KV.Open() }
func (db *DB) Close() error { return db.KV.Close() }

// Relational CRUD mapping to underlying KV operations
func (db *DB) Select(schema *Schema, row Row) (ok bool, err error)
func (db *DB) Insert(schema *Schema, row Row) (updated bool, err error)
func (db *DB) Upsert(schema *Schema, row Row) (updated bool, err error)
func (db *DB) Update(schema *Schema, row Row) (updated bool, err error)
func (db *DB) Delete(schema *Schema, row Row) (deleted bool, err error)
```

## System Constraints & Operational Boundaries

The introduction of these APIs establishes strict rules for how data moves from the relational layer down to the storage layer:

* **Mutation Operations (`Insert`, `Upsert`, `Update`):** The input `Row` must be *complete*. The system requires all column data to accurately serialize the row value. We leverage the `UpdateMode` constraints built in Chapter 0203 to enforce logical correctness at the KV layer.
    * *Example:* `Insert` uses `ModeInsert` to guarantee no primary key collisions.
    ```go
    func (db *DB) Insert(schema *Schema, row Row) (updated bool, err error) {
        key := row.EncodeKey(schema)
        val := row.EncodeVal(schema)
        return db.KV.SetEx(key, val, ModeInsert)
    }
    ```
* **Retrieval & Deletion (`Select`, `Delete`):** The input `Row` only requires the primary key column(s) to be populated. For example, in a table `(a int64, b int64, primary key (b))`, a `Select` operation takes a `Row` of length 2 where column 2 contains the lookup key, and column 1 will be populated by decoding the returned KV value.
* **Access Limitations:** At this specific chapter, the database *only* supports single-row access via the Primary Key. 

## Memory Layout & Serialization Impact

The structural alignment here relies heavily on `row.EncodeKey(schema)` and `row.EncodeVal(schema)`. The `DB` layer does not care about LittleEndian bit-shifting or `fsync` loops; it delegates all byte-level layout logic to the `Row`/`Schema` encoders, which format the bytes precisely for the B+Tree nodes.

## Future Architecture Note

By isolating this primary-key CRUD logic into the `DB` struct, we have created the necessary foundation for the next series of puzzle pieces. Future chapters will expand this interface to support broader relational features:
* Full table scans
* Secondary index queries
* Range queries (by index or PK)
* Result filtering
