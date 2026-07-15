# 0401: Sorting and Search

## 1. Architectural Transition: Beyond Point Lookups

A primitive Key-Value (KV) engine built on top of an unordered Hash Map handles point lookups ($O(1)$ operations like `GET`, `SET`, and `DEL`) with high efficiency. However, hash tables lack structural order, making them incapable of supporting fundamental relational database capabilities. 

To bridge the gap between a simple KV store and a relational database engine, the underlying storage engine must transition to an **ordered data architecture**. This ordering natively enables three essential database features:

* **Table Scans:** Reading every record in a collection sequentially. In an ordered system, a full table scan is architecturally identical to a range query bounded by infinity: $-\infty < \text{key} < +\infty$.
* **Range Queries:** Extracting a subset of records satisfying inequality constraints (e.g., selecting all posts where `123 < post_id AND post_id < 456`). 
* **Result Sorting (`ORDER BY`):** Producing records sorted by a specific attribute. When the underlying storage layout is already sorted, executing an `ORDER BY` statement does not require an active runtime sort phase; the engine simply configures the iterator to scan either forward or backward.

### The True Complexity of Range Queries
A range query is **not** strictly $O(N)$, unless the query specifically requests the entire table. The accurate time complexity for a range query on an ordered structure is **$O(\log N + K)$**, where:
* **$N$** = Total number of records in the database.
* **$K$** = The specific number of records that satisfy the query constraints.

The engine executes this in two phases:
1. **The Initial Lookup ($O(\log N)$):** The engine uses binary search to find the lower bound. It ignores the rest of the database, costing logarithmic time.
2. **The Sequential Scan ($O(K)$):** Once the pointer lands on the first valid record, it leverages the physical sorted order, iterating forward and reading records one-by-one until it hits the upper bound.

If a table has 1,000,000,000 rows, but the query only matches 10 rows, the engine does not scan a billion rows. Finding the start takes $\approx 30$ operations ($O(\log N)$), and scanning takes 10 operations ($O(K)$), for a total of $\approx 40$ operations.

---

## 2. The Sorted Array O(N) Insertion Trap

While a flat **Sorted Array** allows fast binary search reads, it is highly inefficient for dynamic production workloads. Contiguous memory means you cannot simply "drop" a new element into the middle; the CPU must physically make room for it by shifting existing data.

Imagine a database storing 5 social media `post_id`s, sorted in memory:

    keys := [][]byte{
        []byte("post:10"),
        []byte("post:20"), 
        []byte("post:30"), // <-- We want to insert "post:25" here
        []byte("post:40"),
        []byte("post:50"),
    }

To insert `post:25`, the engine finds the insertion index (2) via binary search. To inject the new key, the CPU must physically copy and move every element from index 2 to the end of the array one slot to the right:

    // Shifting trailing elements rightward
    keys[4] = keys[3] // Moves "post:50" to index 4
    keys[3] = keys[2] // Moves "post:40" to index 3
    keys[2] = []byte("post:25") // Injects new key

**The Mathematical Reality:** If the database has 10 elements, shifting 5 is trivial. If the database has $N = 1,000,000,000$ (one billion) rows, inserting a row in the dead center requires the CPU to physically relocate 500 million elements in memory. Statistically, a random insert requires $N/2$ shifts. In Big-O notation, dropping the constant leaves an **$O(N)$ write penalty**, which will cause a database to freeze under load.

---

## 3. Escaping O(N) with Trees: "Nested" and "Leveled" Arrays

Both B+Trees and LSM-Trees bypass the $O(N)$ array-shifting penalty while retaining the $O(\log N)$ binary search speed by breaking one massive array into smaller, manageable arrays.

### The B+Tree: "Nested Arrays"
Instead of one continuous array of a billion elements, a B+Tree breaks the data into thousands of tiny, bounded arrays (usually the size of a standard OS disk page, e.g., 4KB).

    type BTreeNode struct {
        keys     [][]byte     // A small sorted array (e.g., max 4 elements)
        children []*BTreeNode // Pointers to other nodes
    }

When inserting a new key, the engine binary searches down the tree to find the correct leaf node and inserts the key into that *specific* tiny array. Because the array is capped at a tiny size, the $O(N)$ shift is localized. Shifting 2 items inside a 4-item array is effectively $O(1)$ overhead. If the array gets too full, it "splits" into two separate arrays.

### The LSM-Tree: "Multi-Layered Arrays"
An LSM-Tree takes a completely different approach optimized for extreme write throughput by refusing to shift elements on disk entirely.

    type MemTable struct {
        activeArray [][]byte // Small in-memory sorted array
    }
    
    type Disk struct {
        level0 [][][]byte // Immutable, frozen arrays on disk
    }

All new writes go to a small, fast in-memory array (`activeArray`). When this array reaches a size limit, the engine freezes it, marks it as immutable, and writes the entire sorted array to disk as a new file in `level0`. Because it never inserts into the middle of an existing disk file, there is **zero array shifting**. All writes are pure appends.

---

## 4. In-Memory Memory Layout & Schema

To transition away from an unordered Go `map`, the engine introduces a twin-slice contiguous array layout inside the core `KV` structure. This intermediate design serves as the algorithmic foundation for the future LSM-Tree implementation.

    type KV struct {
        log  Log
        keys [][]byte
        vals [][]byte
    }

* **`keys` Field:** A multi-dimensional byte slice (`[][]byte`) containing the record identifiers. This slice must be maintained in strict, deterministic lexicographical order.
* **`vals` Field:** A multi-dimensional byte slice (`[][]byte`) holding the raw payload data. 
* **Index Alignment:** The layout relies on exact structural alignment. The key located at `kv.keys[i]` maps directly to the value stored at `kv.vals[i]`.

---

## 5. Algorithmic Mechanics: Binary Search & Element Placement

Data retrieval and structural modification within this layout shift entirely to **Binary Search**. Instead of scanning the entire array sequentially ($O(N)$ linear time), the engine repeatedly halves the search space, checking the middle element to establish boundaries ($O(\log N)$ logarithmic time).

The Go standard library provides `slices.BinarySearchFunc`, which accepts the target key and a comparison function.

    func BinarySearchFunc(x S, target T, cmp func(E, T) int) (pos int, ok bool)

The boolean return value (`ok`) indicates whether the exact key exists. However, the integer return value (`pos`) behaves as a multi-mode pointer depending on the state of `ok`:

* **If `ok == true`:** `pos` represents the exact index where the key exists. Used by `GET` operations to retrieve data from `kv.vals[pos]`.
* **If `ok == false`:** `pos` represents the **exact insertion index** required to maintain sorted order. Used by `SET` operations to insert the new key/value pair into the memory layout via downstream APIs like `slices.Insert()`.

---

## 6. Cold-Start Database Initialization

Because this flat array structure lacks an index-on-disk tracking mechanic, the engine cannot perform selective or lazy loading from storage during startup. 

When `kv.Open()` executes, the engine must perform a complete linear recovery:
1. Open the append-only write-ahead log (`Log`) from persistent storage.
2. Read every individual log entry sequentially from the beginning of the stream to the end.
3. Reconstruct the sorted arrays in-memory by parsing the keys, evaluating their correct ordered offsets, and populating the `keys` and `vals` slices fully before accepting incoming client traffic.
