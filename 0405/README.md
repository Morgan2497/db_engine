# Chapter 0405: Range Query

## Overview: From Open-Ended Seeks to Bounded Scans

Chapter 0404 introduced `RowIterator`, which bridges the physical KV layer and the relational row layer. Its starting point is `Seek()`:

```go
func (kv *KV) Seek(key []byte) (*KVIterator, error)
```

`Seek(key)` finds the first stored key greater than or equal to `key`. That supports an open-ended query such as:

```text
key >= 123
```

However, SQL queries commonly have both a lower and an upper bound:

```sql
SELECT * FROM users
WHERE id >= 100 AND id <= 200;
```

Chapter 0405 adds a bounded range iterator that:

- starts at one encoded key;
- stops at another encoded key;
- scans in ascending or descending order;
- supports `<`, `<=`, `>`, and `>=`;
- supports full composite primary keys and partial key prefixes.

### At a glance

The chapter adds this pipeline:

```text
RangeReq using logical Cells
            ↓
EncodeKeyPrefix with a synthetic ±∞ suffix
            ↓
KV.Range(start, stop, desc)
            ↓
RangedKVIter enforces the stop boundary
            ↓
RowIterator decodes each KV entry into a Row
```

The central idea is that a range query is not a new storage mechanism. It is a carefully bounded wrapper around the sorted `KVIterator` built in earlier chapters.

---

## 1. Why `Seek()` Alone Is Not Enough

The existing `Seek()` operation answers one question:

> Where is the first key that is greater than or equal to this target?

Given sorted keys:

```text
10, 20, 30, 40, 50
```

calling `Seek(25)` positions the iterator at `30`:

```text
10, 20, [30], 40, 50
```

The caller can continue with `Next()`, but nothing tells it to stop. A query intended to return `25 <= key <= 40` would accidentally continue to `50` unless every caller manually checked the upper bound.

This chapter centralizes that responsibility in a new API:

```go
func (kv *KV) Range(start, stop []byte, desc bool) (*RangedKVIter, error)
```

The range is inclusive at the KV layer:

| Direction | `desc` | Valid keys | Movement |
|---|---:|---|---|
| Ascending | `false` | `start <= key && key <= stop` | `Next()` |
| Descending | `true` | `start >= key && key >= stop` | `Prev()` |

The caller must supply the bounds in traversal order:

```text
Ascending:  start <= stop
Descending: start >= stop
```

---

## 2. `RangedKVIter`: A Boundary-Aware Wrapper

The range iterator wraps the existing `KVIterator` instead of duplicating its cursor logic:

```go
type RangedKVIter struct {
    iter KVIterator
    stop []byte
    desc bool
}
```

Each field has one responsibility:

- `iter` stores the current physical position in the sorted KV arrays.
- `stop` is the inclusive final key allowed in the scan.
- `desc` selects forward or backward traversal.

The current key and value are delegated directly to the wrapped iterator:

```go
func (iter *RangedKVIter) Key() []byte {
    return iter.iter.Key()
}

func (iter *RangedKVIter) Val() []byte {
    return iter.iter.Val()
}
```

The wrapper does not copy the data or reimplement KV access. Its main job is deciding whether the current position is still inside the requested range.

---

## 3. The Stop Condition in `Valid()`

`RangedKVIter.Valid()` first checks the physical iterator and then compares the current key with the stop key:

```go
func (iter *RangedKVIter) Valid() bool {
    if !iter.iter.Valid() {
        return false
    }

    r := bytes.Compare(iter.iter.Key(), iter.stop)

    if iter.desc && r < 0 {
        return false
    } else if !iter.desc && r > 0 {
        return false
    }

    return true
}
```

`bytes.Compare(current, stop)` returns:

| Result | Meaning |
|---:|---|
| `< 0` | `current < stop` |
| `0` | `current == stop` |
| `> 0` | `current > stop` |

### Ascending scan

For an ascending scan, the iterator becomes invalid after it moves above `stop`:

```text
start = 20, stop = 40

20 → 30 → 40 → 50
               valid  invalid
```

The equality case is valid, so the stop bound is inclusive.

### Descending scan

For a descending scan, the iterator becomes invalid after it moves below `stop`:

```text
start = 40, stop = 20

40 → 30 → 20 → 10
               valid  invalid
```

Again, equality with `stop` remains valid.

---

## 4. Direction-Aware Movement

`Next()` means “advance in query order,” not necessarily “move to a larger key”:

```go
func (iter *RangedKVIter) Next() error {
    if !iter.Valid() {
        return nil
    }

    if iter.desc {
        return iter.iter.Prev()
    }

    return iter.iter.Next()
}
```

This gives the relational layer one consistent loop for both `ORDER BY ... ASC` and `ORDER BY ... DESC`:

```go
for iter.Valid() {
    use(iter.Key(), iter.Val())

    if err := iter.Next(); err != nil {
        return err
    }
}
```

The loop does not need to know whether the physical cursor is moving forward or backward.

---

## 5. Initializing a Range from `Seek()`

`KV.Range()` begins by reusing `Seek(start)`:

```go
func (kv *KV) Range(start, stop []byte, desc bool) (*RangedKVIter, error) {
    iter, err := kv.Seek(start)
    if err != nil {
        return nil, err
    }

    // Correct the initial position for descending traversal.
    // Wrap iter and return the RangedKVIter.
}
```

### Ascending initialization

Ascending traversal wants the first key satisfying:

```text
key >= start
```

That is exactly what `Seek(start)` returns, so no correction is needed.

Example:

```text
Stored keys: 10, 20, 30, 40
Start:       25
Seek(25):    30
```

### Descending initialization

Descending traversal wants the first key satisfying:

```text
key <= start
```

But `Seek(start)` still returns the first key satisfying `key >= start`. Its result may therefore be one position too far to the right.

Example:

```text
Stored keys: 10, 20, 30, 40
Start:       35
Seek(35):    40       // too large for a descending range
Correct to:  30       // call Prev() once
```

The correction is required when:

- `Seek(start)` lands on a key strictly greater than `start`; or
- `Seek(start)` lands at `len(keys)` because `start` is greater than every stored key.

If `Seek(start)` finds an exact match, the iterator must remain there:

```text
Stored keys: 10, 20, 30, 40
Start:       30
Seek(30):    30       // exact match; do not call Prev()
```

A compact implementation strategy is:

```go
if desc && (!iter.Valid() || bytes.Compare(iter.Key(), start) > 0) {
    if err := iter.Prev(); err != nil {
        return nil, err
    }
}
```

After this adjustment, the iterator can be wrapped with `stop` and `desc`.

---

## 6. The Composite-Key Prefix Problem

A table can have a composite primary key:

```text
PRIMARY KEY (a, b, c)
```

Because Chapter 0403 made each cell encoding order-preserving, complete tuples can be compared directly:

```text
(a, b, c) <= (123, 4, 5)
```

Range queries may also provide only a prefix. A prefix means that the first columns are specified, but the remaining columns are unspecified:

```text
(a, b) <= (123, 4)
a      <= 123
```

The issue is not merely that the key lengths differ. The KV layer can compare byte arrays of different lengths. The real issue is that the omitted suffix needs a defined meaning: should it behave like the smallest possible value or the largest possible value?

For example:

```text
(a, b) <= (123, 4)
```

means that every full key beginning with `(123, 4)` must be included, regardless of `c`. In full-tuple form, the bound is:

```text
(a, b, c) <= (123, 4, +∞)
```

By contrast:

```text
(a, b) < (123, 4)
```

must exclude every key beginning with `(123, 4)`. Its full-tuple boundary behaves like:

```text
(a, b, c) <= (123, 4, -∞)
```

---

## 7. Why Prefix Ranges Need Infinity

Consider:

```sql
department <= "Sales"
```

This should include every Sales employee:

```text
("Engineering", 1)
("Engineering", 2)
("Sales", 1)
("Sales", 5)
("Sales", 9)
```

But the query only specifies the first primary-key column. It does not provide
`employee_id`.

The database turns the incomplete boundary into:

```text
("Sales", +∞)
```

Every real Sales key sorts before that boundary:

```text
("Sales", 1) < ("Sales", +∞)
("Sales", 5) < ("Sales", +∞)
("Sales", 9) < ("Sales", +∞)
```

Therefore, all Sales rows are included.

For a strict comparison:

```sql
department < "Sales"
```

No Sales row should be included, so the boundary becomes:

```text
("Sales", -∞)
```

Every real Sales key sorts after that boundary:

```text
("Sales", -∞) < ("Sales", 1)
("Sales", -∞) < ("Sales", 5)
("Sales", -∞) < ("Sales", 9)
```

Consequently, stopping at `("Sales", -∞)` stops immediately before the Sales
group.

### The four rules

For an upper bound:

```text
<  prefix  → prefix + -∞
<= prefix  → prefix + +∞
```

For a lower bound:

```text
>= prefix  → prefix + -∞
>  prefix  → prefix + +∞
```

Or as a table:

| Condition | Synthetic boundary | Reason |
|---|---|---|
| `< Sales` | `(Sales, -∞)` | Stop before all Sales rows |
| `<= Sales` | `(Sales, +∞)` | Stop after all Sales rows |
| `>= Sales` | `(Sales, -∞)` | Start before all Sales rows |
| `> Sales` | `(Sales, +∞)` | Start after all Sales rows |

A useful mental shortcut:

```text
-∞ = beginning of the matching prefix group
+∞ = end of the matching prefix group
```

Then ask whether the scan should begin or end before or after that group.

### Example 1: A normal numeric range

Suppose the stored primary keys are:

```text
10, 20, 30, 40, 50
```

And the query is:

```sql
WHERE id >= 20 AND id < 40
```

#### Lower bound: `id >= 20`

Because equality should be included, construct:

```text
(20, -∞)
```

Conceptually:

```text
(20, -∞) < stored key 20
```

Seeking to the first key at or after that boundary lands on `20`.

#### Upper bound: `id < 40`

Because equality should be excluded, construct:

```text
(40, -∞)
```

Conceptually:

```text
(40, -∞) < stored key 40
```

The range iterator accepts keys up through the synthetic stop boundary. When
it reaches the real key `40`, that key is already greater than `(40, -∞)`, so
iteration stops.

Result:

```text
20, 30
```

### Example 2: Changing strictness

Now consider:

```sql
WHERE id > 20 AND id <= 40
```

The boundaries become:

```text
start = (20, +∞)
stop  = (40, +∞)
```

The ordering around those keys is conceptually:

```text
stored 20 < (20, +∞)
stored 40 < (40, +∞)
```

Therefore:

- seeking at or after `(20, +∞)` skips `20`;
- the real `40` is still before `(40, +∞)`, so it is included.

Result:

```text
30, 40
```

Here are all four variants:

| Query | Encoded start | Encoded stop | Result |
|---|---|---|---|
| `id >= 20 AND id <= 40` | `(20, -∞)` | `(40, +∞)` | `20, 30, 40` |
| `id > 20 AND id <= 40` | `(20, +∞)` | `(40, +∞)` | `30, 40` |
| `id >= 20 AND id < 40` | `(20, -∞)` | `(40, -∞)` | `20, 30` |
| `id > 20 AND id < 40` | `(20, +∞)` | `(40, -∞)` | `30` |

Although these look like two-element tuples, with a complete primary key the
“infinity” is effectively positioned immediately before or after that exact
key.

### Example 3: The composite-key case

Suppose the stored keys are:

```text
("Engineering", 1)
("Sales", 1)
("Sales", 5)
("Sales", 9)
("Support", 1)
```

The query is:

```sql
WHERE department >= "Sales"
  AND department <= "Sales"
```

This is essentially “give me every employee in Sales.”

The lower boundary is:

```text
("Sales", -∞)
```

The upper boundary is:

```text
("Sales", +∞)
```

So the complete ordering looks like:

```text
("Engineering", 1)

("Sales", -∞)       ← range begins
("Sales", 1)
("Sales", 5)
("Sales", 9)
("Sales", +∞)       ← range ends

("Support", 1)
```

Result:

```text
("Sales", 1)
("Sales", 5)
("Sales", 9)
```

This is where infinity is especially valuable: the engine can select an
entire composite-key prefix without knowing the minimum or maximum possible
`employee_id`.

---

## 8. Encoding Synthetic Infinity

The chapter uses byte ordering to represent the synthetic suffixes:

```text
-∞ = no suffix bytes
+∞ = 0xff
```

For this to work, the beginning of every real column must sort between those two representations.

The key encoding therefore adds a one-byte type tag before every encoded cell:

```go
key = append(key, byte(cell.Type))
key = cell.EncodeKey(key)
```

The known cell types begin with small bytes such as `0x01` and `0x02`, never `0xff`. At the point where another real column would begin, the ordering is therefore:

```text
no byte (-∞) < type byte (real column) < 0xff (+∞)
```

This gives the desired ordering:

```text
encoded(prefix, -∞)
    < encoded(prefix, any real suffix)
    < encoded(prefix, +∞)
```

### Important encoding change

Full row keys now have this shape:

```text
[table][0x00]
[type][encoded PK cell 1]
[type][encoded PK cell 2]
...
[0x00 full-key terminator]
```

The corresponding implementation is:

```go
func (row Row) EncodeKey(schema *Schema) []byte {
    key := append([]byte(schema.Table), 0x00)

    for _, idx := range schema.PKey {
        cell := row[idx]
        key = append(key, byte(cell.Type))
        key = cell.EncodeKey(key)
    }

    return append(key, 0x00)
}
```

### Why the final `0x00` is necessary

Without a full-key terminator, a complete tuple could have exactly the same bytes as that tuple followed by synthetic `-∞`, because `-∞` is represented by adding nothing.

The final `0x00` separates a real complete key from the empty `-∞` boundary:

```text
encoded tuple with -∞ boundary:  [tuple bytes]
real complete tuple:             [tuple bytes][0x00]
tuple with +∞ boundary:          [tuple bytes][0xff]
```

Therefore:

```text
[tuple] < [tuple][0x00] < [tuple][0xff]
```

This distinction is what makes `<`, `<=`, `>`, and `>=` work for both full keys and prefixes.

---

## 9. Encoding a Key Prefix

`EncodeKeyPrefix` serializes the supplied cells and then chooses a synthetic suffix:

```go
func EncodeKeyPrefix(schema *Schema, prefix []Cell, positive bool) []byte {
    key := append([]byte(schema.Table), 0x00)

    for _, cell := range prefix {
        key = append(key, byte(cell.Type))
        key = cell.EncodeKey(key)
    }

    if positive {
        key = append(key, 0xff)
    }

    return key
}
```

The `positive` flag means:

| `positive` | Suffix | Meaning |
|---:|---|---|
| `false` | nothing | `-∞` |
| `true` | `0xff` | `+∞` |

For a production-quality implementation, this function should also verify that:

- `len(prefix) <= len(schema.PKey)`;
- every prefix cell matches the corresponding primary-key column type;
- prefix cells follow primary-key order.

These checks prevent malformed logical bounds from producing misleading physical keys.

---

## 10. The Relational Range API

The KV layer understands encoded byte bounds. The DB layer needs a logical API based on cells and comparison operators.

The chapter introduces:

```go
type ExprOp uint8

const (
    OP_LE ExprOp = 12 // <=
    OP_GE ExprOp = 13 // >=
    OP_LT ExprOp = 14 // <
    OP_GT ExprOp = 15 // >
)
```

And a range request:

```go
type RangeReq struct {
    StartCmp ExprOp
    StopCmp  ExprOp
    Start    []Cell
    Stop     []Cell
}
```

`Start` and `Stop` may each be:

- a complete primary key;
- a primary-key prefix;
- prefixes of different lengths.

The direction is determined by `StartCmp`:

| `StartCmp` | Direction | Meaning at the starting edge |
|---|---|---|
| `OP_GE` | ascending | begin with keys `>= Start` |
| `OP_GT` | ascending | begin with keys `> Start` |
| `OP_LE` | descending | begin with keys `<= Start` |
| `OP_LT` | descending | begin with keys `< Start` |

Valid request shapes normally pair the starting comparison with an opposite stop comparison:

```text
Ascending:  key >=/> Start, then stop at key <=/< Stop
Descending: key <=/< Start, then stop at key >=/> Stop
```

Examples:

```text
Ascending inclusive:  StartCmp=GE, StopCmp=LE
Ascending exclusive:  StartCmp=GT, StopCmp=LT
Descending inclusive: StartCmp=LE, StopCmp=GE
Descending exclusive: StartCmp=LT, StopCmp=GT
```

---

## 11. Building `DB.Range()`

The DB-level range function performs four jobs:

1. Convert the logical start comparison into an encoded start bound.
2. Convert the logical stop comparison into an encoded stop bound.
3. Determine the traversal direction.
4. Wrap the ranged KV iterator in a row-decoding iterator.

Its structure is:

```go
func (db *DB) Range(schema *Schema, req *RangeReq) (*RowIterator, error) {
    start := EncodeKeyPrefix(
        schema,
        req.Start,
        suffixPositive(req.StartCmp),
    )

    stop := EncodeKeyPrefix(
        schema,
        req.Stop,
        suffixPositive(req.StopCmp),
    )

    desc := isDescending(req.StartCmp)

    kvIter, err := db.KV.Range(start, stop, desc)
    if err != nil {
        return nil, err
    }

    // Create RowIterator, decode the initial entry, and return it.
}
```

The helper truth tables are:

```text
suffixPositive(OP_LT) = false
suffixPositive(OP_LE) = true
suffixPositive(OP_GT) = true
suffixPositive(OP_GE) = false
```

```text
isDescending(OP_LE) = true
isDescending(OP_LT) = true
isDescending(OP_GE) = false
isDescending(OP_GT) = false
```

---

## 12. Updating `RowIterator`

Chapter 0404 stored a raw `KVIterator`:

```go
type RowIterator struct {
    schema *Schema
    iter   *KVIterator
    valid  bool
    row    Row
}
```

Chapter 0405 replaces it with a ranged iterator:

```go
type RowIterator struct {
    schema *Schema
    iter   *RangedKVIter
    valid  bool
    row    Row
}
```

The higher-level iteration pattern remains the same:

```go
for iter.Valid() {
    row := iter.Row()
    use(row)

    if err := iter.Next(); err != nil {
        return err
    }
}
```

This is an important abstraction benefit: the SQL or relational layer does not need separate loops for ascending scans, descending scans, or bounded scans.

---

## 13. Complete Mock Example: Ascending Integer Range

Assume the KV store contains these logical keys:

```text
10, 20, 30, 40, 50
```

The query is:

```sql
WHERE id >= 20 AND id <= 40
ORDER BY id ASC
```

The DB request is conceptually:

```go
RangeReq{
    StartCmp: OP_GE,
    StopCmp:  OP_LE,
    Start:    []Cell{intCell(20)},
    Stop:     []Cell{intCell(40)},
}
```

Execution proceeds as follows:

```text
1. OP_GE chooses -∞ for the start suffix.
2. OP_LE chooses +∞ for the stop suffix.
3. OP_GE selects ascending traversal.
4. KV.Range calls Seek(encoded start).
5. Seek positions the iterator at key 20.
6. Valid accepts 20 because 20 <= stop.
7. Next moves to 30.
8. Next moves to 40.
9. Next moves to 50.
10. Valid rejects 50 because it is above stop.
```

Result:

```text
20, 30, 40
```

---

## 14. Complete Mock Example: Composite Prefix

Assume the primary key is:

```text
(accountID, year, sequence)
```

Stored keys include:

```text
(7, 2024, 1)
(7, 2024, 2)
(7, 2024, 99)
(7, 2025, 1)
(8, 2024, 1)
```

Suppose the query wants every row whose first two primary-key columns are at most `(7, 2024)`:

```text
(accountID, year) <= (7, 2024)
```

Because the comparison is `<=`, the encoder uses `+∞`:

```text
(7, 2024, +∞)
```

All real sequence values sort below that boundary:

```text
(7, 2024, 1)  < (7, 2024, +∞)
(7, 2024, 2)  < (7, 2024, +∞)
(7, 2024, 99) < (7, 2024, +∞)
```

Therefore, every row with prefix `(7, 2024)` is included. The next prefix, `(7, 2025)`, is above the boundary and is excluded.

If the comparison were strict `<`, the encoder would use `-∞`:

```text
(7, 2024, -∞)
```

Every real `(7, 2024, sequence)` key sorts above that boundary, so all rows with that prefix would be excluded.

---

## 15. Important Invariants and Edge Cases

### Empty KV store

`Seek()` may return an iterator positioned at `len(keys)`. `Range()` and `Valid()` must handle this without calling `Key()` or `Val()` on an invalid iterator.

### Start beyond the largest key

For an ascending scan, the iterator is immediately invalid. For a descending scan, `Prev()` should move it to the final stored key if that key is within the stop boundary.

### Start before the smallest key

For an ascending scan, `Seek()` returns the first key. For a descending scan, correcting a too-large `Seek()` result may move the cursor to `-1`, correctly producing an empty result.

### Exact descending start

If `Seek(start)` finds an exact match, descending initialization must not call `Prev()`. The exact start key may be part of an inclusive range.

### Reversed bounds

An ascending request with `start > stop`, or a descending request with `start < stop`, should produce an empty iterator or a clear validation error. The API should choose and document one behavior consistently.

### Table boundaries

Encoded range bounds include `schema.Table + 0x00`, so a correctly formed range remains in one table namespace. The `RowIterator` prefix check from Chapter 0404 remains valuable as a second layer of protection.

### Encoding compatibility

Adding type tags and the final full-key `0x00` changes the physical key format from Chapter 0404. Existing keys written using the old format are not byte-compatible unless they are migrated or rebuilt.

---

## 16. Recommended Tests

The implementation should test at least these cases:

1. Ascending range with exact start and stop matches.
2. Ascending range where neither bound exists.
3. Descending range with exact start and stop matches.
4. Descending range where `Seek(start)` lands above the desired start.
5. Descending range where `start` is above the largest stored key.
6. Empty database.
7. Single-key range where `start == stop`.
8. Empty result caused by reversed bounds.
9. Full composite-key comparisons for all four operators.
10. Partial-prefix comparisons for all four operators.
11. Prefixes of different lengths in `Start` and `Stop`.
12. A range ending at the boundary between two tables.
13. Invalid prefix length or mismatched cell type.
14. Ascending and descending scans returning the same set in opposite order.

---

## Crucial Information and Takeaways

- **`Seek()` supplies a position; `Range()` supplies a position and a stopping rule.** A bounded scan is implemented by wrapping the existing cursor rather than replacing the KV engine.
- **Traversal direction changes both movement and initialization.** Ascending scans use `Next()`, descending scans use `Prev()`, and descending scans may need to correct the initial `Seek()` result.
- **The KV range is inclusive.** SQL-style strict comparisons are represented by choosing a `-∞` or `+∞` encoded suffix before entering the KV layer.
- **Composite-key prefixes require synthetic infinity.** Missing key columns are interpreted as smaller than every real suffix or larger than every real suffix, depending on the comparison operator.
- **Type tags reserve `0xff` for positive infinity.** Every real suffix begins with a known type byte, allowing `0xff` to sort above it.
- **The full-key terminator distinguishes a real complete tuple from `-∞`.** This makes exact full-key comparisons and prefix comparisons share one encoding system.
- **The relational iterator interface stays simple.** Callers continue using `Valid()`, `Row()`, and `Next()` regardless of direction or range boundaries.

Chapter 0405 turns the open-ended row scan from Chapter 0404 into the bounded, ordered scan required by SQL range predicates and `ORDER BY` execution.
