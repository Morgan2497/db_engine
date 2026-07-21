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

A database engine does not create a separate physical file or independent storage instance for every table. Instead, **all tables share a single, unified KV store namespace**. 
* The rows for the `users` table, the `orders` table, and the `products` table are interleaved and stored together in sorted order based on their raw byte keys.

### The Danger of Blind Scanning
If our storage engine executes a range scan for the `users` table starting at ID 100, the physical `KVIterator` will continuously stream raw bytes forward. 
* Once it exhausts all user records, a naive iterator would keep marching forward into the byte space belonging to the `orders` table. 
* Without boundary protection, the query engine would silently corrupt the `users` query results with `orders` data, causing catastrophic data leaks across table namespaces.

**The Solution:** We must inject a **Table-Prefix Guard** directly into our decoding logic. Every table's key is prepended with its table name and a null separator (e.g., `schema.Table + "\x00"`). As the iterator scans, it must dynamically inspect incoming byte keys and immediately halt the moment a key steps outside the target table's namespace.

In an embedded database engine, **all tables share a single, unified Key-Value store**. Because primary keys are serialized into raw byte arrays and sorted lexicographically using `bytes.Compare()`, the physical storage layer has no concept of relational tables. It just sees one massive, continuous stream of sorted bytes.

### Mock Physical Storage View
Imagine your KV store contains rows for two different tables: `users` and `orders`. Because `u` comes alphabetically before `o`, their raw physical bytes look like this on disk:

```text
[Key: "users\x00\x00\x00\x00\x00\x00\x00\x01"] -> [Value: Alice]
[Key: "users\x00\x00\x00\x00\x00\x00\x00\x02"] -> [Value: Bob]
--- (Logical Table Boundary: End of Users, Start of Orders) ---
[Key: "orders\x00\x00\x00\x00\x00\x00\x00\x01"] -> [Value: Item 99]
[Key: "orders\x00\x00\x00\x00\x00\x00\x00\x02"] -> [Value: Item 100]
```

### The Problem Without a Prefix Guard
If a client executes a range scan on the `users` table, the physical `KVIterator` starts at User 1 and streams forward. Once it outputs User 2, a naive iterator would keep marching forward blindly, reading the next physical key on disk: `"orders\x00..."`. 

Without protection, your database would silently leak `orders` data into your `users` query results.

### The Solution: The Table-Prefix Guard
To prevent this, every table key is prepended with its table name plus a null terminator (`schema.Table + "\x00"`). When the iterator evaluates an incoming key, it checks:

```go
if string(key[:len(schema.Table)+1]) != schema.Table+"\x00" {
    return ErrOutOfRange
}
```

If the read-head lands on `"orders\x00..."` while scanning the `users` schema, the prefix check fails immediately, triggering a graceful stop.

To understand why a blind scan causes leaks, let's look at how raw bytes are ordered on disk in a unified Key-Value store.

### The Raw Disk Layout
Remember that an embedded database stores *all* tables in one single KV store. Because keys are sorted lexicographically by their raw bytes, the `users` table and the `orders` table sit right next to each other:

```text
Physical Disk Address | Raw Key Bytes                          | Value
-----------------------------------------------------------------------
Disk Block #101       | "users\x00\x00\x00\x00\x00\x00\x00\x01" | Alice
Disk Block #102       | "users\x00\x00\x00\x00\x00\x00\x00\x02" | Bob
--- END OF USERS TABLE ------------------------------------------------
Disk Block #103       | "orders\x00\x00\x00\x00\x00\x00\x00\x01" | Laptop ($999)
Disk Block #104       | "orders\x00\x00\x00\x00\x00\x00\x00\x02" | Phone ($699)
```

### The Blind Scan Walkthrough
1. You execute a range query: `SELECT * FROM users WHERE id >= 1`.
2. The storage engine seeks to `"users\x00...1"` and streams forward.
3. It reads Block #101 (`users...1`) $\rightarrow$ Valid user (Alice).
4. It reads Block #102 (`users...2`) $\rightarrow$ Valid user (Bob).
5. **The Edge:** The iterator finishes Bob and calls `Next()` to grab the next item on disk. The physical cursor moves to Block #103.
6. Block #103 contains key: `"orders\x00\x00\x00\x00\x00\x00\x00\x01"`.

**Without the Prefix Guard / `ErrOutOfRange` check:** 
A naive iterator doesn't check the table name prefix. It treats Block #103 as just another user, decodes the laptop order ID as if it were a user ID, and returns a corrupted row to your SQL engine (`users` query accidentally returns order data).

**With the Prefix Guard:**
When the decoder looks at Block #103, it checks: does `"orders\x00..."` start with `"users\x00"`? **No.** 
It immediately returns `ErrOutOfRange`, the `RowIterator` sets `iter.valid = false`, and the loop safely terminates. Your `users` query stops cleanly right at the boundary line.
---

## 2. The Architectural Solution: `ErrOutOfRange` as Control Flow
To handle boundary violations cleanly without crashing the query engine, we introduce a specialized sentinel error:

    var ErrOutOfRange = errors.New("out of range")

When the iterator evaluates a raw key from the underlying storage, it passes it through an updated `DecodeKey()` method:

    func (row Row) DecodeKey(schema *Schema, key []byte) (err error) {
        if len(key) < len(schema.Table)+1 {
            return ErrOutOfRange
        }
        if string(key[:len(schema.Table)+1]) != schema.Table+"\x00" {
            return ErrOutOfRange
        }
        // ... proceed with standard column decoding
    }

When we write `len(key) < len(schema.Table) + 1`, we are checking if a raw byte slice fetched from disk is **physically too short to possibly belong to our table**. 

### The Math & The Mock Example
Let’s trace this using the `users` table:
*   `schema.Table` = `"users"`
*   `len(schema.Table)` = 5 (because `'u', 's', 'e', 'r', 's'` takes 5 bytes)
*   Our prefix format requires the table name **plus** a null byte separator (`\x00`). 
*   Therefore, the absolute minimum length a valid key for the `users` table can *ever* have is:
    $$\text{Minimum Length} = 5 \text{ (table name)} + 1 \text{ (null byte)} = 6 \text{ bytes}$$

Now, imagine the physical KV store contains a stray key that is only 3 bytes long (for example, a system key or leftover metadata like `[]byte("abc")`). 

### What happens if you *don't* have this length check?
Look at the very next line of code in `DecodeKey()`:
```go
if string(key[:len(schema.Table)+1]) != schema.Table+"\x00" {
    return ErrOutOfRange
}
```
If `key` is `[]byte("abc")` (length 3), and you try to slice it up to index 6 (`key[:6]`), **Go will instantly crash your program** with a fatal runtime panic:
```text
panic: runtime error: slice bounds out of range [[:6] with length 3]
```

### The Two Reasons for this Check:
1. **Crash Prevention (The Safety Guard):** It stops Go from throwing an out-of-bounds memory panic when trying to slice a key that is shorter than our expected prefix size.
2. **Logical Fast-Fail:** If a key on disk has a length of 3 bytes, it is mathematically impossible for it to be a `users` table key (which needs at least 6 bytes just for the prefix). Instead of panicking or trying to parse garbage data, it politely returns `ErrOutOfRange` so the iterator knows to skip it.

### Why `ErrOutOfRange` is Not a System Failure
In standard software engineering, an error usually indicates an exceptional failure (such as a disk read failure, null pointer, or network timeout). 
* In database execution engines, **`ErrOutOfRange` is a vital control-flow primitive.** 
* It acts as a polite boundary signal. When the decoding layer returns `ErrOutOfRange`, it tells the iterator: *"You have hit the physical end of this table's contiguous storage space. Stop scanning."*

## 2. `ErrOutOfRange` as Control Flow

In standard application development, errors usually represent catastrophic failures (like a database crash, missing file, or network timeout). In a database storage engine, **`ErrOutOfRange` is a communication primitive (control flow)**.

### Mock Scenario: Hitting the Table Edge
1. The iterator is scanning the `users` table and processes `[Key: "users\x00...2"]`. Success.
2. The iterator calls `Next()` and moves to the next physical key on disk: `[Key: "orders\x00...1"]`.
3. The decoding function inspects the prefix, sees that `"orders"` does not match `"users"`, and returns `ErrOutOfRange`.
4. Instead of crashing the query, the `RowIterator` intercepts this, sets its internal `valid` flag to `false`, and tells the SQL execution loop: *"The scan is complete. Stop pulling rows."*

---

## 3. The RowIterator State Machine & Caching Design
A raw `KVIterator` provides low-level primitive movements (`Next()`, `Seek()`). However, querying a table requires frequent checks to see if the iterator is still valid and requests for the current row's data. 

If every call to `iter.Row()` or `iter.Valid()` had to re-scan or re-decode the underlying byte stream, the database CPU would bottleneck instantly on repetitive parsing overhead.

### Structuring the `RowIterator`
To solve this, the `RowIterator` is designed as a **cached state wrapper**. It stores the decode results directly inside its own memory struct:

    type RowIterator struct {
        schema *Schema
        iter   *KVIterator
        valid  bool // Cached decode result (true if err != ErrOutOfRange)
        row    Row  // Cached decoded row data
    }

### The Methods: O(1) Memory Accessors
Because the state is cached in the struct, the interface methods exposed to the query planner become trivial, ultra-fast O(1) memory lookups:

    // Is iteration finished?
    func (iter *RowIterator) Valid() bool { 
        return iter.valid 
    }

    // Current row accessor
    func (iter *RowIterator) Row() Row { 
        return iter.row 
    }

---

## Behind the Scenes: The Execution Mechanics of `Next()`

When the query engine wants to advance to the next record, it calls `iter.Next()`. This method orchestrates the physical-to-relational translation pipeline in a strict, sequential order:

    func (iter *RowIterator) Next() (err error) {
        // Step 1: Advance the raw storage pointer in the underlying KV engine
        if err = iter.iter.Next(); err != nil {
            return err
        }
        
        // Step 2: Decode the raw bytes and check table boundary limits
        iter.valid, err = decodeKVIter(iter.schema, iter.iter, iter.row)
        return err
    }

When building database iterators, developers often make the mistake of performing computations inside getter methods (e.g., parsing raw bytes every time `Row()` is called). This creates massive CPU bottlenecks.

### The Anti-Pattern (On-Demand Parsing - Slow)
If `Row()` looked like this:
```go
// BAD: Re-decodes bytes from disk on every single call
func (iter *RowIterator) Row() Row {
    return decodeBytesIntoRow(iter.iter.Value()) 
}
```
If your SQL execution engine evaluates a row 50 times across different filters and projections, it would re-parse the raw byte slice 50 times, wasting precious CPU cycles on repetitive binary math.

### The Cached Architecture ($O(1)$ Lookups - Fast)
Instead, our `RowIterator` uses a **cached state wrapper**. It does the heavy decoding work **once** inside `Next()`, stores the resulting Go struct directly in its own memory struct, and serves subsequent requests instantly.

#### Mock Memory State During Iteration
When `iter.Next()` runs successfully, the struct holds this exact state in RAM:

```go
// State in RAM after a successful Next() call
iter := &RowIterator{
    schema: &Schema{Table: "users"},
    iter:   <pointer to underlying KVIterator at disk position 2>,
    valid:  true,
    row:    Row{Fields: []Value{{Type: TypeI64, I64: 2}, {Type: TypeString, Str: "Bob"}}},
}
```

When the SQL query engine calls `iter.Valid()` or `iter.Row()`, there is **no disk I/O, no byte scanning, and no decoding math**. It is a direct, instantaneous $O(1)$ memory fetch:

```go
// Is iteration finished? (Direct boolean read in RAM)
func (iter *RowIterator) Valid() bool { 
    return iter.valid // returns true
}

// Current row accessor (Direct struct read in RAM)
func (iter *RowIterator) Row() Row { 
    return iter.row // returns Row{Fields: ...}
}
```

This caching strategy ensures that the storage layer's physical byte-streaming is cleanly decoupled from the relational layer's memory models, maximizing throughput during high-speed range scans.

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
