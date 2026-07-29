# 0401: Sorting and Search

> **Course map:** Storage goes ordered (`keys[]` / `vals[]`); SQL and `DB` API stay the same. See [repo README](../README.md) for how 04xx fits after 0305.

## Overview: What This Chapter Changes

Through **0305**, the engine is fully functional: SQL parses, `ExecStmt` runs, rows encode/decode, and the KV layer persists via log + CRC + `fsync`. **0401 changes only `kv.go`** — the storage basement.

```text
0305                              0401
────                              ────
KV.mem = map[string][]byte   →    KV.keys [][]byte  (sorted)
                                  KV.vals [][]byte  (parallel values)

Same public API:  Get / Set / SetEx / Del / Open / Close
Same above KV:    sql_parser.go, table.go, row.go, ExecStmt
```

| Layer | Changed in 0401? |
| :--- | :---: |
| SQL parser + AST | No |
| `ExecStmt` / `table.go` | No |
| `Row.EncodeKey` / `EncodeVal` | No |
| Log, CRC, tombstones | No |
| **`KV` in-memory layout** | **Yes** |

**Milestone tests:** `go test -v` — especially `TestKVRecovery` (new `Open()` compaction) and `TestSQLByPKey` (proves SQL still works after reopen).

---

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

## 1. B+Tree: "Nested Arrays"

**Concept:** Breaks data into tiny, bounded arrays (pages). Insertion shifts elements only within the specific small page.

**Scenario:** Inserting `10, 20, 30, 40, 5` with a **max node size of 3**.

### Step 1: Initial Inserts
Keys fit into the root.
```text
Root: [10, 20, 30]
```

### Step 2: Insert 40 (Split)
Overflow triggers a split. The middle key moves up.
```text
      [20]
     /    \
[10, 20]  [30, 40]
```

### Step 3: Insert 5 (Localized Shift)
Search goes left. Only the tiny leaf array is modified.
```text
      [20]
     /    \
[5, 10, 20]  [30, 40]
```
**Result:** Shifting is **O(1)** relative to the total dataset because it happens only inside a 4KB page.

---

## 2. LSM-Tree: "Multi-Layered Arrays"

**Concept:** Writes go to an in-memory array (MemTable). When full, the whole array is flushed to disk as an **immutable** file. No shifting ever occurs on disk.

**Scenario:** Inserting `10, 20, 5, 30, 15` with a **MemTable limit of 3**.

### Step 1: Memory Writes
Data accumulates in memory.
```text
MemTable: [5, 10, 20]
```

### Step 2: Flush to Disk
Limit reached. The array freezes and writes to **Level 0** as a new file.
```text
Disk (Level 0): [ [5, 10, 20, 30] ]  <-- Immutable File
MemTable:       []                   <-- Empty, ready for new writes
```

### Step 3: Overlapping Files
New writes create a second file. Files are **not** merged immediately.
```text
Disk (Level 0):
  File A: [5, 10, 20, 30]
  File B: [15, 25, 35]     <-- Overlaps with File A
```

### Step 4: Compaction
A background process merges files later.
```text
Disk (Level 1): [ [5, 10, 15, 20, 25, 30, 35] ] <-- Merged & Sorted
```
**Result:** Write path is **pure append**. Expensive merging is deferred to the background.

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


## 7. High-Level Architecture
The read path leverages logarithmic time binary search across the strictly ordered keys array. If the search yields a valid index, the engine retrieves the corresponding payload from the vals array via direct spatial alignment.

```
kv := &KV{
	log: Log{},
	keys: [][]byte{
		[]byte("post:10"),
		[]byte("post:20"),
		[]byte("post:30"),
	},
	vals: [][]byte{
		[]byte(`{"author":"alice"}`),
		[]byte(`{"author":"bob"}`),
		[]byte(`{"author":"charlie"}`),
	},
}
key := []byte("post:25")
```

Binary search on `keys` for `"post:25"` → `idx=1`, `ok=false` (insertion index between `"post:20"` and `"post:30"`).

```text
Get("post:20"):
  BinarySearchFunc(keys, "post:20", bytes.Compare)
  → idx=1, ok=true
  → return vals[1]   // O(log N)

SetEx("post:25", val, ModeUpsert) when key is new:
  → idx=1, exist=false
  → log.Write(...)
  → slices.Insert(keys, 1, "post:25")
  → slices.Insert(vals, 1, val)   // O(N) shift — see §2
```

---

## 8. What Changed vs 0305 (`kv.go` only)

| Piece | 0305 | 0401 |
| :--- | :--- | :--- |
| Struct | `mem map[string][]byte` | `keys [][]byte` + `vals [][]byte` |
| `Open()` | Replay log in file order → map | Read all → **sort by key** → **compact** → arrays |
| `Get` | `mem[string(key)]` — O(1) | `slices.BinarySearchFunc` — O(log N) |
| `SetEx` insert | `mem[k]=v` | `slices.Insert` at sorted index |
| `SetEx` update | `mem[k]=v` | `vals[idx]=v` in place (no shift) |
| `Del` | `delete(mem,k)` | `slices.Delete` at index |

`SetEx` mode semantics (`ModeInsert` / `ModeUpdate` / `ModeUpsert`) are **unchanged** from 0203.

---

## 9. `Open()` — Sort + Compact (New Recovery Path)

0305 replayed the log **in append order**; last write per key wins naturally. Sorted arrays have no random-access-by-key during replay, so 0401 uses a three-phase rebuild:

```text
Phase 1 — READ ALL
  entries = every log record (messy history)

Phase 2 — SORT BY KEY (stable)
  slices.SortStableFunc(entries, bytes.Compare on key)
  same key → entries stay in chronological order

Phase 3 — COMPACT
  walk sorted entries; for each key, latest entry wins
```

### Hand trace (`TestKVRecovery`)

Log in append order:

```text
1. Set  user1 → "Morgan"
2. Set  user2 → "Alice"
3. Set  user1 → "Morgan Kim"
4. Del  user2
```

After stable sort by key:

```text
user1 → "Morgan"        (older)
user1 → "Morgan Kim"    (newer)
user2 → "Alice"
user2 → (tombstone)
```

Compaction walk:

| Step | Entry | Action | `keys` after |
| :---: | :--- | :--- | :--- |
| 1 | user1="Morgan" | append | `[user1]` |
| 2 | user1="Morgan Kim" | pop previous same key, append | `[user1]` val="Morgan Kim" |
| 3 | user2="Alice" | append | `[user1, user2]` |
| 4 | user2 deleted | pop user2, skip append | `[user1]` |

The pop logic:

```go
if n > 0 && bytes.Equal(kv.keys[n-1], ent.key) {
    kv.keys, kv.vals = kv.keys[:n-1], kv.vals[:n-1]
}
```

After sorting, duplicate keys are **adjacent**. Stable sort preserves time order within each key group.

---

## 10. Operation Walkthroughs

### `Get` — point lookup

```go
idx, ok := slices.BinarySearchFunc(kv.keys, key, bytes.Compare)
if ok { return kv.vals[idx], true, nil }
```

| `ok` | `idx` means |
| :---: | :--- |
| `true` | Exact index of `key` |
| `false` | Insertion index to keep sorted order |

Example: `keys = ["a", "c", "f"]` — search `"b"` → `idx=1`, `ok=false`.

### `SetEx` — insert vs update

```text
idx, exist := BinarySearchFunc(...)

exist=true  → log.Write → vals[idx] = val     (update in place)
exist=false → log.Write → slices.Insert keys/vals at idx
```

Mode truth table (same as 0203):

| Mode | Key missing | Key exists, same val | Key exists, new val |
| :--- | :--- | :--- | :--- |
| Upsert | insert | no-op | overwrite |
| Insert | insert | no-op | no-op |
| Update | no-op | no-op | overwrite |

### `Del` — tombstone + slice delete

```text
1. BinarySearchFunc → idx, exist
2. if !exist → return false
3. log.Write(tombstone) FIRST
4. slices.Delete(keys, idx, idx+1)
5. slices.Delete(vals, idx, idx+1)
```

Remaining keys stay sorted.

---

## 11. SQL Still Works — End-to-End Stack

An `INSERT` through 0401 is identical to 0305 above the KV layer:

```text
INSERT INTO link VALUES (123, 'bob', 'alice')
  → parseStmt (0304) → StmtInsert
  → execInsert (0305)
  → db.Insert (0204) → EncodeKey + EncodeVal
  → kv.SetEx(..., ModeInsert) (0203)
  → ★ binary search + slices.Insert ★ (0401)
  → log.Write + fsync (0103–0105)
```

`TestSQLByPKey` closes and reopens the DB — if `Open()` compaction is wrong, DELETE after reopen fails even when in-memory ops looked fine.

### Multiple tables — not a schema scan

All tables share one KV, but row keys are **namespaced** by table prefix (`EncodeKey` prepends `table + 0x00`). A `SELECT … FROM link WHERE pk=…` does **not** search all schemas:

```text
GetSchema("link")           → 1 lookup (@schema_link or RAM cache)
EncodeKey → "link\0bob\0alice"
KV.Get(exact key)           → 1 lookup (not random, not all 4 tables)
```

---

## 12. Complexity Cheat Sheet

| Operation | Map (0305) | Sorted array (0401) |
| :--- | :--- | :--- |
| Get | O(1) | O(log N) |
| Update existing | O(1) | O(log N), no shift |
| Insert new | O(1) | O(log N) + **O(N) shift** |
| Delete | O(1) | O(log N) + **O(N) shift** |
| Range scan | impossible | enabled in 0402+ |
| `Open()` replay | O(entries) | O(entries log entries) sort |

0401 trades hash-map speed for **order**. B-trees and LSM-trees (§3) exist to keep order without O(N) shifts on every insert.

---

## 13. Tests to Run

```bash
cd 0401 && go test -v
```

| Test | What it proves |
| :--- | :--- |
| `TestKVBasic` | set/get/del on sorted storage |
| `TestKVUpdateMode` | `ModeInsert` / `ModeUpdate` / `ModeUpsert` |
| `TestKVRecovery` | **new** sort+compact `Open()` |
| `TestTableByPKey` | relational CRUD unchanged |
| `TestSQLByPKey` | full SQL + reopen after 0401 storage |

---

## 14. What's Next (0402)

0401 gives **sorted data** but no way to walk it yet. Chapter **0402** adds:

```text
kv.Seek(key) → *KVIterator
iter.Next() / iter.Prev()
iter.Key() / iter.Val()
```

That is when table scans and range queries become possible at the storage layer. **0403** fixes sort-correct encoding; **0404** wraps iterators into table-scoped `RowIterator`.
