# 0203: Update Modes

## The Semantic Gap: SQL vs. KV
Up until this point, our KV store only natively understood a blind "overwrite" operation. When we map SQL operations to KV, most map cleanly (`SELECT` → `Get()`, `DELETE` → `Del()`). 

However, SQL has strict semantics for writing data:
* `INSERT`: Must **fail** if the row (Primary Key) already exists.
* `UPDATE`: Must **fail** (or do nothing) if the row does not exist.

A standard KV `Set()` does an `INSERT ... ON DUPLICATE UPDATE` (an "Upsert"). If we don't fix this at the storage layer, the upper SQL execution layer would have to perform a `Get()` before every `Set()` to check existence, which is slow and introduces race conditions. 

This chapter solves that by pushing the existence constraints down into the KV layer via `SetEx()`.

---

## Atomic Concepts

### 1. The `UpdateMode` Primitive
We are introducing a strongly typed enum to represent the intent of a write operation. We stop treating all writes as blind overwrites and start treating them as conditional mutations.

```go
type UpdateMode int

const (
    ModeUpsert UpdateMode = 0 // Insert or Overwrite (The old default)
    ModeInsert UpdateMode = 1 // Strict Insert (Fails if exists)
    ModeUpdate UpdateMode = 2 // Strict Update (Fails if not exists)
)
```

### 2. The `SetEx` Interface
We expand the KV interface to accept this new primitive. 
`func (kv *KV) SetEx(key []byte, val []byte, mode UpdateMode) (bool, error)`

* **Returns `bool`**: Indicates whether the operation actually mutated the tree (e.g., if `ModeInsert` fails, it returns `false`).
* **Returns `error`**: Standard error handling for disk/system issues.

---

## Constraints (The Rules of the Chapter)

When implementing the logic inside the B-Tree / KV store, the following rules **must** be strictly enforced when evaluating a key during a write:

1. **`ModeInsert` (Insert New)**
   * **Rule**: If the key ALREADY EXISTS → Do nothing, return `false`.
   * **Rule**: If the key DOES NOT EXIST → Insert the node, return `true`.

2. **`ModeUpdate` (Update Existing)**
   * **Rule**: If the key ALREADY EXISTS → Overwrite the value, return `true`.
   * **Rule**: If the key DOES NOT EXIST → Do nothing, return `false`.

3. **`ModeUpsert` (Insert or Update)**
   * **Rule**: If the key ALREADY EXISTS → Overwrite the value, return `true`.
   * **Rule**: If the key DOES NOT EXIST → Insert the node, return `true`.

---

```

### Implementation Strategy
1. Try `ModeInsert` on a key. Assert it works.
2. Try `ModeInsert` on the *same* key. Assert it returns `false`.
3. Try `ModeUpdate` on a non-existent key. Assert it returns `false`.
4. Try `ModeUpdate` on the key from step 1. Assert it works.
5. Try `ModeUpsert` on both an existing and non-existing key. Assert both work.

---

## Performance Trade-offs
**Why do this in the KV layer instead of the SQL layer?**
If the SQL execution engine handled this, it would have to do:
1. `Get(key)` (requires a full tree traversal down to the leaf).
2. Evaluate if it should proceed.
3. `Set(key)` (requires a *second* full tree traversal down to the leaf to write).

By pushing `UpdateMode` into `SetEx()`, the KV engine only traverses the tree **once**. When it reaches the leaf node, it immediately knows whether it exists or not, evaluates the `UpdateMode`, and applies the write in place. This cuts disk I/O and CPU overhead for tree traversal exactly in half.
