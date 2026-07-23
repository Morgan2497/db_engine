# Chapter 0404: Row Iterator

## Overview: Bridging the Physical KV Layer to the Relational Layer
In Chapter 0403, we solved the physical storage sorting problem by implementing order-preserving serialization, ensuring that our Key-Value (KV) store stores primary keys as sorted, contiguous byte sequences. However, raw bytes alone cannot power a relational database engine.

A modern database engine operates across two distinct architectural abstractions:
1. **The Physical/Storage Layer:** Operates via a raw `KVIterator`, which blindly streams raw byte slices (`[]byte` keys and values) across disk blocks or memory tables using simple lexicographical byte-by-byte comparisons (`bytes.Compare()`). It has zero awareness of schemas, data types, columns, or tables.
2. **The Relational/Logical Layer:** Operates via structured `Row` objects, executing operations like filtering, projecting, and evaluating SQL clauses.

**The Architectural Gap:** When we execute a range query (e.g., `SELECT * FROM users WHERE id > 100`), the query engine cannot hand raw byte streams directly to the SQL parser. It requires an intermediate translation mechanism that can convert raw KV streams into structured `Row` objects while strictly respecting table boundaries, handling decoding control flows, and maintaining an efficient memory state machine. 

This chapter introduces the **Row Iterator**, the core architectural component that bridges the raw `KVIterator` to the relational `Row` model.

---

## 1. The Multi-Table Co-Location Problem (The Namespace Bug)
To understand why an advanced iterator wrapper is necessary, we must examine how an embedded database engine stores multiple tables. 

A database engine does not create a separate physical file or independent storage instance for every table. Instead, **all tables share a single, unified KV store namespace**. Because primary keys are serialized into raw byte arrays and sorted lexicographically using `bytes.Compare()`, the physical storage layer has no concept of relational tables. It just sees one massive, continuous stream of sorted bytes where the rows for different tables are interleaved.

### Mock Physical Storage View
Imagine your KV store contains rows for two different tables: `users` and `orders`. Because `u` comes alphabetically before `o`, their raw physical bytes look like this on disk:

```text
Physical Disk Address | Raw Key Bytes                          | Value
-----------------------------------------------------------------------
Disk Block #101       | "users\x00\x00\x00\x00\x00\x00\x00\x01" | Alice
Disk Block #102       | "users\x00\x00\x00\x00\x00\x00\x00\x02" | Bob
--- END OF USERS TABLE ------------------------------------------------
Disk Block #103       | "orders\x00\x00\x00\x00\x00\x00\x00\x01" | Laptop ($999)
Disk Block #104       | "orders\x00\x00\x00\x00\x00\x00\x00\x02" | Phone ($699)
```

### Why a "Blind Scan" is Dangerous (The Analogy)
To see why blind scanning is a bug, look at how a SQL query actually knows when to finish. When you run a query like:

```sql
SELECT * FROM users WHERE id >= 1;
```

The database SQL engine doesn't magically know how many users exist. Instead, it enters a continuous loop using our iterator:

```go
for iter.Valid() {
    row := iter.Row()
    fmt.Println(row)
    iter.Next() // moves to the next item on disk
}
```

#### The Analogy: Reading a Book Without Chapter Headers
Imagine Chapter 1 (`users`) and Chapter 2 (`orders`) are printed back-to-back in a massive, unformatted book. You are assigned to read *only* Chapter 1.
* **Without the Prefix Guard:** You read User 1, User 2... and reach the end of Chapter 1. Your read-head crosses the invisible line and enters Chapter 2 (`orders`). Because you aren't checking for boundaries, you keep reading: *"Order #1: Laptop, Order #2: Phone..."* thinking they are still users. You end up dumping financial orders into a report meant only for user profiles.
* **With the Prefix Guard:** The moment your read-head touches the first byte of Chapter 2 (`orders\x00...`), the decoder says, *"Wait, this doesn't start with `users\x00`!"* It triggers `ErrOutOfRange`, stops the loop immediately, and your `users` query finishes cleanly without leaking order data.

### The Solution: The Table-Prefix Guard
To prevent data leaks, every table key is prepended with its table name plus a null terminator (`schema.Table + "\x00"`). As the iterator scans, it dynamically inspects incoming byte keys and immediately halts the moment a key steps outside the target table's namespace.

---

## 2. `ErrOutOfRange` as Control Flow & Length Safety Checks
To handle boundary violations cleanly without crashing the query engine, we introduce a specialized sentinel error:

```go
var ErrOutOfRange = errors.New("out of range")
```

When the iterator evaluates a raw key from the underlying storage, it passes it through an updated `DecodeKey()` method:

```go
func (row Row) DecodeKey(schema *Schema, key []byte) (err error) {
    if len(key) < len(schema.Table)+1 {
        return ErrOutOfRange
    }
    if string(key[:len(schema.Table)+1]) != schema.Table+"\x00" {
        return ErrOutOfRange
    }
    // ... proceed with standard column decoding
}
```

### Why `len(key) < len(schema.Table) + 1` is Required
When we write `len(key) < len(schema.Table) + 1`, we are checking if a raw byte slice fetched from disk is **physically too short to possibly belong to our table**. 

* **The Math & Example:** For the `users` table, `schema.Table` has a length of 5 bytes (`'u', 's', 'e', 'r', 's'`). Adding the null byte separator (`\x00`), the minimum required length is $5 + 1 = 6$ bytes. If a stray system key or metadata key of length 3 bytes appears on disk, it cannot possibly be a valid table key.
* **Crash Prevention:** Without this check, attempting to slice the key (`key[:len(schema.Table)+1]`) on an undersized key would cause Go to throw a fatal runtime panic (`slice bounds out of range`).
* **Logical Fast-Fail:** It safely catches invalid key formats and converts them into an `ErrOutOfRange` signal rather than crashing.

### Why `ErrOutOfRange` is Not a System Failure
In standard software engineering, an error usually indicates an exceptional failure (such as a disk read failure, null pointer, or network timeout). In database execution engines, **`ErrOutOfRange` is a vital control-flow primitive.** It acts as a polite boundary signal telling the iterator that it has hit the physical end of the table's contiguous storage space and should stop scanning.

### Mock Scenario: Hitting the Table Edge
1. The iterator is scanning the `users` table and processes `[Key: "users\x00...2"]`. Success.
2. The iterator calls `Next()` and moves to the next physical key on disk: `[Key: "orders\x00...1"]`.
3. The decoding function inspects the prefix, sees that `"orders"` does not match `"users"`, and returns `ErrOutOfRange`.
4. Instead of crashing the query, the `RowIterator` intercepts this, sets its internal `valid` flag to `false`, and tells the SQL execution loop: *"The scan is complete. Stop pulling rows."*

---

## 3. The RowIterator State Machine & Caching Design
A raw `KVIterator` provides low-level primitive movements (`Next()`, `Seek()`). However, querying a table requires frequent checks to see if the iterator is still valid and requests for the current row's data. 

### Structural Definition
To avoid repetitive parsing overhead, the `RowIterator` is designed as a **cached state wrapper**. It stores the decode results directly inside its own memory struct:

```go
type RowIterator struct {
    schema *Schema
    iter   *KVIterator
    valid  bool // Cached decode result (true if err != ErrOutOfRange)
    row    Row  // Cached decoded row data
}
```

### The $O(1)$ Memory Accessors
Because the state is cached in the struct, the interface methods exposed to the query planner become trivial, ultra-fast $O(1)$ memory lookups instead of re-parsing bytes from disk on every getter call:

```go
// Is iteration finished? (Direct boolean read in RAM)
func (iter *RowIterator) Valid() bool { 
    return iter.valid 
}

// Current row accessor (Direct struct read in RAM)
func (iter *RowIterator) Row() Row { 
    return iter.row 
}
```

### Why Caching Matters: Evaluating a Row Within a Single Query
When you write a SQL query, it rarely just fetches raw data and hands it to you. It passes through multiple internal processing steps called **query operators**. Consider this single query:

```sql
SELECT name, age 
FROM users 
WHERE age > 20 AND status = 'active' 
ORDER BY name;
```

When the database engine processes a single row (for example, **User #2: Bob, Age 25, Status 'active', Email 'bob@email.com', Address '123 Main St'**), that row has to pass through several internal checks:
1. **The Filter Operator (`age > 20`):** Inspects Bob's age field.
2. **The Second Filter Operator (`status = 'active'`):** Inspects Bob's status field.
3. **The Projection Operator (`SELECT name, age`):** Strips away his email and address fields, keeping only name and age.
4. **The Sorting Operator (`ORDER BY name`):** Inspects Bob's name to see where he fits in the final sorted output array.

If the iterator did *not* cache the decoded row in memory, every single one of those internal operators would have to re-read Bob's raw bytes from disk and re-run all the binary decoding math from scratch just to evaluate him. Caching ensures Bob is decoded **once**, and all operators share that clean Go struct instantly.

### Where the $O(1)$ Memory Fetch Shines (Scanning 100,000 Rows)
This speed advantage shines microsecond by microsecond while scanning a dataset inside a single query. Think about what happens when a query scans **100,000 rows**:
* **The Slow Way (Without Caching):** Every time the engine moves to a new row (`Next()`), it does raw I/O and heavy byte-decoding. Worse, if an operator asks for the row data multiple times, it triggers decoding all over again. Across 100,000 rows, that turns into millions of redundant CPU cycles spent parsing binary data.
* **The Fast Way (With Caching / $O(1)$ Accessors):** 
  1. `iter.Next()` runs once: It moves the physical disk pointer and does the heavy decoding math one time, storing the resulting struct in `iter.row`.
  2. The SQL engine loops through its operators, calling `iter.Row()` over and over. Because `iter.row` is already a native Go struct sitting right there in RAM, fetching it takes zero disk I/O and zero math. It is an instantaneous $O(1)$ memory pointer lookup.

---

## Behind the Scenes: The Execution Mechanics of `Next()`

When the query engine wants to advance to the next record, it calls `iter.Next()`. This method orchestrates the physical-to-relational translation pipeline in a strict, sequential order:

```go
func (iter *RowIterator) Next() (err error) {
    // Step 1: Advance the raw storage pointer in the underlying KV engine
    if err = iter.iter.Next(); err != nil {
        return err
    }
    
    // Step 2: Decode the raw bytes and check table boundary limits
    iter.valid, err = decodeKVIter(iter.schema, iter.iter, iter.row)
    return err
}
```

### The Step-by-Step Lifecycle Trace
1. **The Movement:** `iter.iter.Next()` moves the raw hardware or memory cursor forward to the next physical KV pair in the sorted index.
2. **The Translation (`decodeKVIter`):** The engine takes the new raw key-value pair, extracts the byte array, runs `DecodeKey()`, and verifies whether it hits our table prefix boundary.
3. **The State Mutation:** 
   * If the key belongs to the table, `DecodeKey()` succeeds, `iter.valid` is set to `true`, and `iter.row` is populated with the decoded column values.
   * If the key belongs to another table (or the index ends), `DecodeKey()` returns `ErrOutOfRange`, `iter.valid` is flipped to `false`, and the iteration safely halts without bubbling up a disruptive system error.

---

## Crucial Information & Takeaways

* **Decoupling Storage from Relations:** By wrapping the `KVIterator` inside a `RowIterator`, we maintain a clean architectural boundary. The storage engine remains completely "dumb"—it only knows how to sort and stream raw bytes. The iterator provides the "intelligence" to interpret those bytes into relational rows.
* **Control Flow vs. System Error:** Utilizing `ErrOutOfRange` as a sentinel value allows range queries to terminate gracefully when hitting table boundaries without treating normal table edges as runtime exceptions.
* **The Foundation for Range Queries:** Building the `Seek()` and `RowIterator` pipeline enables our database engine to execute high-performance range scans, setting the direct stage for integrating SQL support and advanced disk-based data structures like LSM-Trees.
