# Chapter 0403: Sort Orders & Order-Preserving Serialization

## Overview: The Database Architecture Layer Gap
To understand this chapter, we first have to look at the "big picture" of database architecture. A modern database is typically split into two distinct layers:
1. **The Relational/Logical Layer:** This understands table schemas, data types (integers, floats, varchars), and SQL.
2. **The Storage/Physical Layer (KV Store):** This is the basement of the engine. It only understands raw arrays of bytes (`[]byte`). 

When we execute a range query (e.g., `SELECT * WHERE age > 20`), the physical KV engine uses a simple byte-by-byte comparison (`bytes.Compare()`) to scan the data. It evaluates these bytes left-to-right, lexicographically. 

**The Problem:** If we take relational data (like a signed `int64` or a dynamically sized string) and serialize it into bytes using standard methods, the resulting raw bytes will often sort in the **wrong logical order**. For instance, negative integers might evaluate as "larger" than positive ones in raw binary, or a shorter string might sort after a longer one due to how length prefixes are evaluated.

## 1. The Variable-Length String Problem (The Prefix Bug)
When building a database index, lexicographical order (dictionary order) must be maintained. If strings are serialized using a standard `[length_prefix][data]` format, the sorting engine will evaluate the length byte first, which completely breaks alphabetical sorting.

### Example: "Z" vs "AA"
* **Logical Alphabetical Order:** "AA" comes before "Z" (`"AA" < "Z"`).
* **The Raw Memory (Length-Prefixed):**
  * "Z" (Length 1, ASCII 90): `[0x01, 0x5A]`
  * "AA" (Length 2, ASCII 65, 65): `[0x02, 0x41, 0x41]`
* **The `bytes.Compare()` Execution:**
  * The KV store compares the very first byte (the length prefix).
  * `0x01` is less than `0x02`.
  * **Outcome:** The database incorrectly sorts "Z" as smaller than "AA".

**The Solution:** This is why databases abandon length prefixes for indexed keys and use C-style **null-terminated strings** combined with escape characters (e.g., escaping literal `0x00` bytes as `0x01 0x01`). This forces the byte comparison to evaluate the actual character data first.

---

## 2. The 64-Bit Signed Integer Problem (Full Bit Representation)
Standard binary uses Two's Complement for negative numbers, setting the Most Significant Bit (MSB) to `1`. Because the KV engine reads bytes blindly from left to right as unsigned magnitudes, negative numbers will structurally appear larger than positive numbers.

### Step-by-Step Example
Let's trace three 64-bit integers (`int64`) in their correct logical order: **-2 < 0 < 2**

**Step 1: The Raw Memory (The Bug)**
If we write these numbers directly to disk in Big-Endian format, here is the exact 64-bit machine representation:
* **-2:** `11111111 11111111 11111111 11111111 11111111 11111111 11111111 11111110` (Hex: `0xFFFFFFFFFFFFFFFE`)
* **0:**  `00000000 00000000 00000000 00000000 00000000 00000000 00000000 00000000` (Hex: `0x0000000000000000`)
* **2:**  `00000000 00000000 00000000 00000000 00000000 00000000 00000000 00000010` (Hex: `0x0000000000000002`)

*Result:* `bytes.Compare()` looks at the first byte. `0xFF` (255) is vastly larger than `0x00` (0). The engine incorrectly sorts them as: **0 < 2 < -2**.

**Step 2: Applying the MSB Flip (The Fix)**
We map the signed space to the unsigned space by flipping only the first bit. We achieve this using a bitwise XOR against `1 << 63` (Hex `0x8000000000000000`).

* **-2:** `01111111 11111111 11111111 11111111 11111111 11111111 11111111 11111110` (Hex: `0x7FFFFFFFFFFFFFFE`)
* **0:**  `10000000 00000000 00000000 00000000 00000000 00000000 00000000 00000000` (Hex: `0x8000000000000000`)
* **2:**  `10000000 00000000 00000000 00000000 00000000 00000000 00000000 00000010` (Hex: `0x8000000000000002`)

*Result:* The engine reads the first byte. `0x7F` (127) < `0x80` (128). The new byte order evaluates as `0x7F... < 0x80...00 < 0x80...02`. The physical byte order now perfectly matches the logical order (**-2 < 0 < 2**).


## Example:
* When KV keys are compared as raw strings or bytes, serialized data types may compare in the wrong order because their byte representations do not preserve logical value ordering (e.g., lexicographic string comparison of "10" vs "2" yields "10" < "2", whereas numeric comparison yields 2 < 10). 

*  Serialized String the raw sequence of bytes produced when a data value (like a number, date, or object) is converted into a linear format for storage or transmission.

In the context of Key-Value (KV) stores and the sorting issues you mentioned:
- Definition: It is the byte representation of a value after an encoding scheme (like JSON, MessagePack, Protocol Buffers, or simple binary casting) is applied.

- The Sorting Trap: When these bytes are compared using standard functions like bytes.Compare(), they are evaluated lexicographically (byte-by-byte from left to right), not by their logical value.

- Example: The integer 10 serialized as a standard string is the bytes ['1', '0']. The integer 2 is ['2'].

- Since the byte 0x31 ('1') is less than 0x32 ('2'), the serialized string for 10 sorts before 2, which is logically incorrect for numeric data.

*  To resolve this, systems must either:

1. Use native type comparison: Ensure keys are stored and compared as their original types (e.g., integers compared numerically) rather than serialized strings. 
2. Apply lexicographic encoding: Convert values into byte sequences that preserve ordering, such as using fixed-width padding for integers (e.g., 00000002 vs 00000010) or IEEE-754 bit-preserving floats for numeric sorting. 
3. Without these adjustments, relying on default byte comparison (bytes.Compare()) leads to incorrect query results when retrieving ranges or sorting keys in embedded KV stores like RocksDB or SQLite.


## The Concept & Theory: Order-Preserving Serialization
Faced with this sorting mismatch, database engineers have two choices:

* **The Anti-Pattern (Coupling):** We could force the KV storage layer to "ask" the relational layer what data type it is looking at, deserialize the bytes back into integers or strings, and then run the comparison. This introduces extreme tight coupling. The storage layer becomes slow, complex, and highly dependent on the schema.
* **The Industry Standard (Decoupling):** We keep the storage layer completely "dumb" and fast. Instead, we design a specialized, mathematical **Order-Preserving Serialization Format**. We format the bytes up in the logical layer so that their raw, left-to-right lexicographical byte order *exactly matches* the natural logical order of the data. 

# Deep Dive: Order-Preserving Serialization for Integers

## The Core Problem: Two's Complement Memory
To understand why standard integer serialization fails in database indexing, we have to look at how computers store negative numbers using a system called **Two's Complement**. 

In Two's Complement, the first bit (the Most Significant Bit, or MSB) acts as the "sign bit." 
* If the MSB is `0`, the number is positive.
* If the MSB is `1`, the number is negative.

Because a database's Key-Value (KV) store evaluates keys using a simple byte-by-byte lexicographical comparison (`bytes.Compare()`), it reads bytes strictly left-to-right, treating them all as unsigned magnitudes. 

If we serialize raw `int64` memory into Big-Endian bytes, a negative number (starting with `1`) will be evaluated by the KV store as **larger** than a positive number (starting with `0`). This completely destroys the database's ability to execute range queries (e.g., `WHERE temperature < 0`), because the physical sort order no longer matches the logical math order.

## The Mathematical Solution: The MSB Flip
To fix this without coupling the KV store to the schema, we must physically alter the binary before saving it. We need to seamlessly map the signed `int64` space onto the unsigned `uint64` space:
* **Original signed `int64` space:** `[-2^63, ..., -1, 0, 1, ..., 2^63 - 1]`
* **Target unsigned `uint64` space:** `[0, ..., 2^63 - 1, 2^63, 2^63 + 1, ..., 2^64 - 1]`

We achieve this mathematically by taking the `int64`, casting it to a `uint64`, and adding `2^63`. In binary, adding `2^63` (which is exactly a `1` followed by 63 `0`s) is identical to flipping the Most Significant Bit. We do this using a bitwise XOR operation: `val ^ (1 << 63)`.

---

## Step-by-Step Example (Miniaturized)
To make this easy to visualize, let's shrink our 64-bit integers (`int64`) down to 8-bit integers (`int8`). 
* In `int8`, the MSB is the 8th bit.
* We flip it using XOR `1000 0000` (which is `1 << 7`).

Let's take three numbers in logical order: **-2, 0, and 2**.

### Step 1: The Raw Memory (The Bug)
Here is how the computer naturally represents these numbers in Two's Complement:
* **-2:** `1111 1110`
* **0:**  `0000 0000`
* **2:**  `0000 0010`

If the KV engine runs `bytes.Compare()` on these raw binaries, it sorts them from lowest binary value to highest:
1. `0000 0000` (Logical 0)
2. `0000 0010` (Logical 2)
3. `1111 1110` (Logical -2)

*Result: The database incorrectly thinks that 0 < 2 < -2.*

### Step 2: Applying the MSB Flip (The Fix)
Before encoding the key, we apply our bitwise XOR: `val ^ 1000 0000`. This flips the first bit of every number, but leaves the other 7 bits identical.

* **-2:** `1111 1110` XOR `1000 0000` = `0111 1110`
* **0:**  `0000 0000` XOR `1000 0000` = `1000 0000`
* **2:**  `0000 0010` XOR `1000 0000` = `1000 0010`

### Step 3: The Order-Preserved Result
Now, let's let the KV engine run `bytes.Compare()` on our newly formatted binary sequences:
1. `0111 1110` (Maps back to Logical -2)
2. `1000 0000` (Maps back to Logical 0)
3. `1000 0010` (Maps back to Logical 2)

*Result: The physical byte order now perfectly mirrors the logical math order (-2 < 0 < 2). Our serialization format is now officially Order-Preserving, and the KV engine can safely and blindly sort primary keys without knowing what an integer is.*
To achieve this, the engine's serialization logic must split into two distinct paths:
* `EncodeVal()`: Used for data payloads. It prioritizes space and speed. It does not care about sorting.
* `EncodeKey()`: Used exclusively for Primary Keys. It translates complex data types into a strict, order-preserving byte sequence so the underlying index (like an LSM-Tree) can traverse it cleanly.

---

## Behind the Scenes: The Engineering Mechanics

### 1. Integer Sorting (The MSB Flip)
When standardizing integers for a byte-by-byte comparison, we must use Big-Endian format so the Most Significant Bits (MSB) are read first. Unsigned integers (`uint64`) naturally sort perfectly this way. 

However, relational databases use signed integers (`int64`), which include a sign bit. In standard binary storage, negative numbers can trigger higher byte values than positive numbers, destroying our sort order.

**The Fix:** We map the signed `int64` space [-2^63, 2^63 - 1] directly onto the unsigned `uint64` space [0, 2^64 - 1]. 
* We accomplish this by flipping the Most Significant Bit (the sign bit) using a bitwise XOR operation: `uint64(val) ^ (1 << 63)`. 
* By flipping this single bit, all negative numbers are mathematically shifted to evaluate as strictly smaller than zero, and all positive numbers are shifted to evaluate as larger. The physical bytes now perfectly mirror the logical integer timeline.

### 2. String Sorting (Null-Termination & Escaping)
Previously, strings were serialized with a length prefix (e.g., `[length, byte, byte]`). This fails for database sorting. If we compare the bytes of `[3, 'A', 'B', 'C']` against `[2, 'Z', 'Y']`, the engine compares the first byte: `3 > 2`. It would incorrectly conclude that "ABC" is greater than "ZY".

**The Fix:** We drop the length prefix and switch to C-style null-terminated strings. We append a `0x00` byte to mark the end of the string.
* If one string is a prefix of another (e.g., "App" vs "Apple"), the shorter string hits the `0x00` terminator first. Because `0x00` is the absolute lowest possible byte value, the engine correctly evaluates "App" as smaller than "Apple".
* **The Escape Hatch:** What if a user inserts a string that actually contains a `0x00` byte? The engine would prematurely think the string ended. We must escape it. We introduce `0x01` as our escape character. We scan the string during encoding:
  * If we see `0x00`, we write `0x01 0x01`.
  * If we see `0x01`, we write `0x01 0x02`.
* This safely removes all natural `0x00` bytes from the data payload, leaving the actual `0x00` reserved exclusively as the structural end-of-string delimiter.

---

## Crucial Information & Takeaways

* **The Power of Composite Keys:** Because we have engineered every individual data type (ints, floats, strings) to perfectly preserve its own sorting order at the byte level, composite primary keys become incredibly easy to implement. We simply serialize Column A, then serialize Column B, and concatenate them together. The resulting combined byte array natively supports tuple-like sorting (e.g., `(a, b) > (c, d)`) without the database needing any complex logic during the actual query execution.

## Example:
PRIMARY KEY (OrderID, ProductID), the database physically stores the row using a single concatenated byte key: [Encodeed OrderID] + [Encoded ProductID]

* Why this is powerful, coming from Physical Locality and Prefix Scanning.
Because of the lexicographical property discussed earlier, this single byte string automatically supports efficient queries on. When you concatenate encoded columns into a single byte string for a Composite Primary Key (e.g., OrderID + ProductID), the databsae stores the rows on the disk stored by that exact byte sequence.

To make this instantly click, let’s start with a real-world analogy before we dive into the binary. 

Imagine a massive physical filing cabinet sorted by a composite key: **`[Last Name] + [First Name]`**. 
* If I ask you to find "Smith, John" (Exact Lookup), you jump straight to the "Smith" drawer and pull John's folder.
* But what if I ask for *everyone* with the last name "Smith" (Prefix Scan)? You don't read the whole cabinet. You jump to the very first "Smith" (maybe "Smith, Aaron"), and you just grab folders sequentially until you hit "Smitty." 

In a database, **Physical Locality** means that all the "Smiths" (or Order `100`s) are physically touching each other on the hard drive. The hard drive's mechanical read head doesn't have to jump around; it just streams the data in one continuous swipe. 

Here is your enhanced, step-by-step breakdown of how the database engine executes this at the binary level.

# The Power of Composite Keys: Binary Encoding & Prefix Scans

## 1. The Setup: Encoding & Concatenation
When we define a composite primary key like `PRIMARY KEY (OrderID, ProductID)`, the database engine does not store two separate columns in the index. It creates a **single, unified byte string**.

To ensure negative numbers sort before positive numbers, the engine applies our **Sign Bit Flip** (XOR with the most significant bit) to both integers *before* gluing them together.

### The Binary Encoding Process (Simplified to 8-bit)
*   **Order 100**: 
    *   Raw Binary: `01100100` 
    *   **Encoded** (Flip first bit): **`11100100`**
*   **Order 101**: 
    *   Raw Binary: `01100101`
    *   **Encoded** (Flip first bit): **`11100101`**
*   **Product 5**: 
    *   Raw Binary: `00000101`
    *   **Encoded** (Flip first bit): **`10000101`**
*   **Product 20**: 
    *   Raw Binary: `00010100`
    *   **Encoded** (Flip first bit): **`10010100`**

To create the final physical key, the engine simply concatenates (glues) them:
`Key = [Encoded OrderID] + [Encoded ProductID]`

--- 

## 2. Exact Lookup: `WHERE OrderID = 100 AND ProductID = 5`

**The Goal:** Find one specific product in one specific order.

*   **Target Construction:**
    The engine takes the two target numbers, encodes them, and glues them together to create a single search string.
    *   `[Enc(100)]`: `11100100`
    *   `[Enc(5)]`: `10000101`
    *   **Search Target**: `11100100 10000101`

*   **Engine Action:**
    The LSM-Tree engine compares this full binary string against its internal structures. Since the binary representation is unique and ordered, it leverages Bloom filters and sparse indexes to jump directly to the exact block within its Sorted String Tables (SSTables) containing this bit pattern.
    *   **Cost**: Highly optimized $O(\log N)$ or $O(1)$ with Bloom filters, rapidly isolating the exact row across storage levels.


2. Prefix Scans (The "Superpower"): WHERE OrderID = 100 (Get all products in an order)
The DB seeks to [Enc(100)] and scans forward becaues this physical arrangement allows the database to answer "Parent" queries incredibly fast without checking every row in the table.
Because OrderID is the prefix, all products for that order are stored contiguously on the disk.

**The Goal:** Fetch *all* products that belong to Order 100.
It stops scanning automatically when it hits a key starting with [Enc(101)].

Because `OrderID` was defined first in our Primary Key, its bits form the **prefix** (the left-side) of the binary string. Therefore, all rows for Order 100 share the exact same starting bits, forcing them to be stored perfectly grouped together on the disk.

### The Physical Disk View
Look at how the identical **prefix** (Order 100) naturally groups the rows, while the **suffix** (ProductID) naturally sorts them within that group.

1.  **`11100100`** `10000101`  ← (Order 100, Product 5)
2.  **`11100100`** `10010100`  ← (Order 100, Product 20)
3.  **`11100100`** `10111110`  ← (Order 100, Product 30)
    *(Physical Boundary: The prefix bits change here)*
4.  **`11100101`** `10000010`  ← (Order 101, Product 2)

### The Mechanism: Calculating the Scan Boundaries
To get these rows, the engine refuses to scan the whole table. Instead, it calculates a Start boundary and a Stop boundary using raw binary math.

1.  **Calculate the Start Key (Inclusive):**
    *   Take the target prefix `[Enc(100)]`: `11100100`
    *   Append the *absolute lowest* possible suffix (all zeros): `00000000`
    *   **Start Target**: `11100100 00000000`
    *   *Result:* The LSM-Tree seeks to this exact point in its index, locating the block right at the beginning of Order 100.

2.  **Calculate the Stop Key (Exclusive):**
    *   Take the target prefix `[Enc(100)]` and **add 1** to it: `11100101` (Which is Order 101).
    *   Append the absolute lowest possible suffix: `00000000`
    *   **Stop Target**: `11100101 00000000`

### Engine Action: The Sequential Scan
Now, the engine's read-head turns on and streams forward byte-by-byte, completely blindly. 

*   **Read 1**: `11100100 10000101` -> Is this smaller than the Stop Target? **Yes.** Yield row.
*   **Read 2**: `11100100 10010100` -> Is this smaller than the Stop Target? **Yes.** Yield row.
*   **Read 3**: `11100100 10111110` -> Is this smaller than the Stop Target? **Yes.** Yield row.
*   **Read 4**: `11100101 10000010` -> Is this smaller than the Stop Target? **No.** The prefix changed. The engine immediately halts.

## 4. Why This Architecture is so Powerful

1.  **Zero Logical Overhead:** During the scan, the database does *not* decode the bits back into integers. It does not run `if order_id == 100`. It acts strictly as a byte-comparator, looking at raw 1s and 0s until it hits a bit pattern that crosses the Stop Target.
2.  **Sequential I/O (The Holy Grail):** Because the prefix forced all Order 100 records to live adjacently, the hard drive reads them in one, unbroken physical sweep. Sequential reads are orders of magnitude faster than random reads on both HDDs and SSDs.
3.  **Automatic Suffix Sorting:** Notice that when we fetched Order 100, the products (5, 20, 30) came back automatically sorted. We did not need an `ORDER BY` clause, saving CPU and memory sorting overhead.

Therefore, It reads only the relevant data in one sequential I/O operation. It doesn't need to know where ProductID ends or perform complex logic; it just reads bytes until the prefix changes. 

--- 
## 4. Range Scans: `WHERE OrderID BETWEEN 100 AND 200` (The Continuous Sweep)

**The Goal:** Fetch every single product across a large span of orders, starting from the very first product in Order 100 and stopping after the very last product in Order 200.

Because our keys are physically sorted on disk in ascending binary order, a range query operates using the exact same mechanical logic as a prefix scan, just with a much wider stopping boundary. 

### The Mechanism: Calculating the Scan Boundaries
In SQL, the `BETWEEN` operator is typically inclusive. We want every product in Order 100, every product in all the orders in between, and every product in Order 200. 

To execute this without reading the entire disk, the engine calculates the extreme outer edges of the range using raw binary:

1.  **Calculate the Start Key (Inclusive):**
    *   Take the bottom boundary prefix `[Enc(100)]`. Let's abstract the binary to `...01100100`.
    *   Append the *absolute lowest* possible suffix (all zeros): `00000000`.
    *   **Start Target**: `...01100100 00000000`
    *   *Result:* The LSM-Tree seeks to this exact point, landing exactly at the beginning of Order 100.

2.  **Calculate the Stop Key (Exclusive):**
    *   Because we want to *include* all of Order 200, we must tell the engine to stop at the exact beginning of Order 201.
    *   Take the top boundary `[Enc(200)]` and **add 1** to it: `[Enc(201)]` (`...11001001`).
    *   Append the absolute lowest possible suffix: `00000000`.
    *   **Stop Target**: `...11001001 00000000`

### Engine Action (The Physical Sweep)
The LSM-Tree seeks to the **Start Target** (`Order 100, Product 0`) using its memtable and SSTable iterators, and turns on the data stream. 

The mechanical read-head sweeps forward sequentially across the disk. It reads Order 100, then naturally flows right into Order 101, Order 102, all the way through Order 200. It blindly streams every single byte it touches into memory, doing nothing but a fast lexicographical comparison against the Stop Target.

*   **Read**: `[Enc(100)] [Enc(5)]` -> Smaller than Stop Target? **Yes.** Yield row.
*   **Read**: `[Enc(150)] [Enc(10)]` -> Smaller than Stop Target? **Yes.** Yield row.
*   **Read**: `[Enc(200)] [Enc(99)]` -> Smaller than Stop Target? **Yes.** Yield row.
*   **Read**: `[Enc(201)] [Enc(1)]` -> Smaller than Stop Target? **No.** The binary boundary is crossed. **STOP.**

---

## Why This Architecture is Powerful for Range Queries
If the database did not use order-preserving binary concatenation, executing `BETWEEN 100 AND 200` would require the engine to perform 101 individual, scattered lookups across the LSM components. 

Instead, by aligning the physical byte layout with the logical sorting order, the database engine transforms a massive multi-order query into a **highly optimized seek, followed by one continuous, high-speed sequential read.** This specific mechanical advantage is exactly how production database engines achieve massive throughput on reporting and financial analytics queries.

* **Preparation for Advanced Indexing:** This chapter is the critical bridge between basic storage and advanced database mechanics. The `Seek(target)` iterator we built previously is entirely reliant on the fact that keys are sorted accurately in memory or on disk. By ensuring our serialization format naturally preserves logical order, we have paved the exact runway needed to transition our data layer into LSM-Trees.

# Deep Dive: Why Order-Preserving Serialization is the Runway for Advanced Indexing

Even though we haven't written the code for LSM-Trees yet, this chapter is the exact architectural prerequisite for them. Here is the reasoning behind why we must solve this sorting problem *now*, before we can build those advanced structures.

## 1. The Mechanics of `Seek(target)` and Binary Search
In Chapter 0402, we transitioned from basic hash maps to ordered arrays, and we built a `Seek(target)` iterator. To make `Seek` fast ($O(\log N)$), we rely on **Binary Search**.

Think about how Binary Search physically operates: it jumps to the middle of the array and asks a simple question: *"Is my target physically larger or smaller than this middle item?"* 
* If smaller, it throws away the right half.
* If larger, it throws away the left half.

**The Catastrophe:** If we do not use order-preserving serialization, the physical bytes will lie to the Binary Search algorithm. If you search for `Logical 10`, but standard serialization causes `Logical 10` to look like a smaller byte-sequence than `Logical 2`, the Binary Search will go *left* instead of *right*. It will look in the wrong half of the array, and your data will simply "vanish" from the database's perspective. 

## 2. The LSM-Tree Connection (Routing and Compaction by Bytes)
An LSM-Tree (which powers modern engines like RocksDB, Cassandra, and LevelDB) is essentially built on massive, disk-based versions of our ordered array called SSTables (Sorted String Tables). 

An LSM-Tree relies on an in-memory MemTable and multiple levels of immutable on-disk SSTables. To route queries without scanning everything, the engine uses sparse indexes and Bloom filters that hold "pivot keys" representing the start and end ranges of data blocks. 

To achieve extreme performance, the LSM-Tree engine must be incredibly "dumb." It does not have time to deserialize bytes into Go `int64` structs or parse SQL strings during rapid memtable flushes or background compactions. It relies strictly on extremely fast `bytes.Compare()` operations. 

If the byte representation does not perfectly match the logical truth, the LSM-Tree routing algorithm will check the completely wrong SSTable blocks, and background compactions will merge data into the wrong sequential order.

## 3. The "Runway" Metaphor
You cannot build a high-speed jet (an LSM-Tree) if your runway is covered in potholes. 

Before we can start building complex tree structures that partition and merge data across a hard drive, we must have absolute, 100% mathematical certainty that if `A < B` logically, then `[]byte(A) < []byte(B)` physically. 

By taking the time in Chapter 0403 to build our `EncodeKey()` methods with MSB-flips and null-terminated strings, we have completely paved the runway. We have guaranteed that our physical storage medium natively obeys logical math. Because of this, when we do start building LSM-Trees in the upcoming chapters, the merging and compaction logic can simply trust the bytes and route queries flawlessly.
