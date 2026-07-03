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


## The `Select` Operation: Primary Key Retrieval

### Overview
The `Select` method is the fundamental retrieval mechanism for the relational layer. It maps a primary key lookup to the underlying `KV.Get()` operation and hydrates a structured `Row` with the returned data.

### System Constraints & Operational Boundaries
* **Partial Input Requirement:** The `Select` function strictly requires the input `Row` to have its primary key column(s) populated before invocation. The remaining data columns are treated as empty buffers to be filled.
* **In-Place Hydration:** By passing `row Row` (which underlyingly acts as a reference to the column data), the function mutates the row in place upon a successful `KV.Get()`. If the key is not found, the operation returns `ok = false` and the row state should be considered invalid or untouched.
* **No Table Scans:** At this stage of the architecture, `Select` enforces a strict $O(1)$ or $O(\log n)$ (depending on the B+Tree depth) exact-match lookup. It cannot scan or filter without the exact primary key.

### Memory Layout & Serialization Impact
The operational flow clearly demarcates the responsibility boundary:
1. `DB.Select` relies on `Row.EncodeKey()` to serialize the primary key according to the `Schema` definitions.
2. The `KV` store blindly retrieves the corresponding raw `val` bytes.
3. `DB.Select` relies on `Row.DecodeVal()` to parse the LittleEndian byte array back into discrete column types (e.g., extracting an `int64` from the byte slice).
This ensures the `DB` struct itself remains entirely ignorant of byte-level memory layouts, acting purely as a traffic controller.


## The `Upsert` Operation: Unconditional Writes

### Overview
The `Upsert` method acts as the relational equivalent of an `INSERT ... ON CONFLICT DO UPDATE` statement. It takes a completely populated `Row`, serializes it, and unconditionally writes it to the underlying storage engine, creating a new record if the primary key does not exist or overwriting the existing record if it does.

### System Constraints & Operational Boundaries
* **Complete Input Requirement:** Unlike `Select`, `Upsert` mandates that all columns in the input `Row` are fully populated. The system relies on this complete state to accurately serialize the `val` payload.
* **Idempotency:** `Upsert` is fundamentally idempotent. Executing the exact same `Upsert` operation multiple times will result in the same final storage state without throwing key collision errors.
* **Return Value Semantics:** The returned `updated` boolean indicates whether a mutation actually occurred at the storage layer. If a row is upserted with data identical to what already exists on disk, `updated` will return `false` to prevent redundant log writes.

### Memory Layout & Serialization Impact
The serialization responsibilities are cleanly bifurcated:
1. `Row.EncodeKey()` isolates and formats the primary key column(s) into the `key` byte slice.
2. `Row.EncodeVal()` packs the remaining non-primary-key columns into a dense `val` byte slice.
3. The `DB` struct remains completely agnostic to the LittleEndian encoding, acting solely to route the serialized slices down to `KV.SetEx(key, val, ModeUpsert)`.

## The `Insert` Operation: Strict Insertion

### Overview
The `Insert` method is the relational equivalent of a standard SQL `INSERT` statement. It takes a completely populated `Row`, serializes it, and writes it to the underlying storage engine *only* if the primary key does not already exist.

### System Constraints & Operational Boundaries
* **Complete Input Requirement:** The `Insert` function requires all columns in the input `Row` to be fully populated to accurately serialize the row's state.
* **Primary Key Constraint Enforcement:** By passing `ModeInsert` down to `KV.SetEx()`, the `DB` layer strictly enforces Primary Key uniqueness. If the encoded key already exists in the B+Tree (or underlying memory map), the operation aborts, protecting existing data from accidental overwrites. 
* **Return Value Semantics:** The `updated` boolean is critical here. A return value of `false` (without an accompanying error) specifically indicates a primary key collision occurred and the row was intentionally rejected.

### Memory Layout & Serialization Impact
The serialization path is identical to `Upsert`:
1. `Row.EncodeKey()` isolates the primary key into the `key` byte slice.
2. `Row.EncodeVal()` packs the remaining columns into the `val` byte slice.
The distinction lies entirely in the logical control flow dictated by `ModeInsert` at the KV layer, ensuring that the previously established memory layout for an existing row remains untouched in the event of a collision.

## The `Delete` Operation: Primary Key Removal

### Overview
The `Delete` method maps a relational row deletion to the underlying KV storage engine. It identifies the target record using the primary key and issues a deletion command, effectively abstracting away the low-level tombstone mechanics of the storage log.

### System Constraints & Operational Boundaries
* **Partial Input Requirement:** Just like `Select`, `Delete` only requires the input `Row` to have its primary key column(s) populated. The state of the remaining data columns is entirely ignored during the operation.
* **Idempotent-ish Execution:** If a deletion is requested for a key that does not exist, the storage engine does not throw an error. It safely ignores the request, avoids a redundant log write, and returns `deleted = false`. 
* **No Cascading or Multi-row Deletions:** At this layer of the architecture, `Delete` strictly enforces a $O(1)$ or $O(\log n)$ single-row removal based on an exact primary key match. Condition-based deletions (e.g., `DELETE FROM wallets WHERE balance < 0`) are not supported by this primitive.

### Memory Layout & Serialization Impact
The operation relies entirely on `Row.EncodeKey()` to format the search bytes. `Row.EncodeVal()` is completely bypassed, as the underlying storage engine only requires the `key` byte slice to append a boolean tombstone marker (`deleted: true`) to the disk log. The `DB` struct remains oblivious to how the KV engine manages memory reclamation or log compaction behind the scenes.

## Example 
- The Mock Setup
  Imagine we are building a financial ledger and we have a wallets table.

  Columns: wallet_id (Primary Key), owner_name, balance

  Schema Definition:

  - Index 0: wallet_id
  - Index 1: owner_name
  - Index 2: balance

PKey: [0] (indicating column 0 is the primary key)

```
```// Mock Input State
targetRow := Row{
    {Type: TypeInt64, Value: 999}, // wallet_id (Populated)
    {Type: TypeString},            // owner_name (Empty buffer)
    {Type: TypeInt64},             // balance (Empty buffer)
}

db.Select(walletSchema, targetRow)```


