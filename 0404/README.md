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

### Why `ErrOutOfRange` is Not a System Failure
In standard software engineering, an error usually indicates an exceptional failure (such as a disk read failure, null pointer, or network timeout). 
* In database execution engines, **`ErrOutOfRange` is a vital control-flow primitive.** 
* It acts as a polite boundary signal. When the decoding layer returns `ErrOutOfRange`, it tells the iterator: *"You have hit the physical end of this table's contiguous storage space. Stop scanning."*

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
