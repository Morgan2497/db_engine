# Chapter 0803: Indexed Query

## What this chapter adds

Chapter 0801 taught the database how to describe indexes.

Chapter 0802 taught it how to maintain the physical entries for those indexes
when rows are inserted, updated, or deleted.

Chapter 0803 finally uses the secondary indexes for queries.

The main change is this:

```text
before 0803:
WHERE conditions can scan only the primary-key namespace

after 0803:
WHERE conditions can select a matching primary or secondary index
```

The chapter also fixes a subtle iterator bug: updating or deleting rows while
iterating over the same sorted structure can invalidate the iterator. The fix
is to collect matching rows first, then mutate the database.

This chapter does not add transactions or crash atomicity. Those are the next
chapter's responsibilities.

---

## The database now has two separate jobs

An index can be used in two stages:

```text
1. Search an ordered index key
2. Recover the complete table row
```

The primary index already performs both jobs in one entry:

```text
primary key -> complete non-primary row value
```

A secondary index performs only the first job:

```text
secondary key -> primary key
```

In this project the primary key is included at the end of every secondary
index key, so the secondary entry can point back to the primary row without
storing the complete row again.

For example:

```sql
CREATE TABLE users (
    id INT64,
    city STRING,
    name STRING,
    INDEX (city),
    PRIMARY KEY (id)
);
```

The schema contains:

```go
schema.Indices = [][]int{
    {0},    // index 0: id, the primary index
    {1, 0}, // index 1: city, id, the secondary index
}
```

For the row:

```text
id=10, city="LA", name="Morgan"
```

the physical entries are conceptually:

```text
primary:
    key   = (index 0, id=10)
    value = city="LA", name="Morgan"

secondary:
    key   = (index 1, city="LA", id=10)
    value = empty
```

The secondary key tells us both the search value (`LA`) and the primary key
(`10`). The database can therefore search the secondary index and then fetch
the complete row through the primary index.

---

## 1. `IndexNo` identifies the key namespace

`IndexNo` is the position of an index inside `Schema.Indices`:

```text
IndexNo 0 -> primary index
IndexNo 1 -> first secondary index
IndexNo 2 -> second secondary index
```

It is not an LSM-tree level. An LSM level describes where a KV entry is stored
inside the storage hierarchy; `IndexNo` describes which logical table index
created that entry.

The encoded key begins with:

```text
table name | 0x00 | index number | encoded index columns | terminator
```

This keeps the namespaces separate:

```text
users | 0x00 | 0x00 | ...  primary entries
users | 0x00 | 0x01 | ...  city-index entries
```

Without the index number, entries from different indexes could overlap in the
same KV key space.

---

## 2. `RangeReq` now carries the selected index

The range request gains an `IndexNo` field:

```go
type RangeReq struct {
    StartCmp ExprOp
    StopCmp  ExprOp
    Start    []Cell
    Stop     []Cell
    IndexNo  int
}
```

The request describes both:

```text
which values form the range
which index should be scanned
```

Example query:

```sql
SELECT id, city FROM users WHERE city >= 'LA' AND city < 'NY';
```

If the city index is index 1, the request is conceptually:

```go
RangeReq{
    StartCmp: OP_GE,
    StopCmp:  OP_LT,
    Start:    []Cell{"LA"},
    Stop:     []Cell{"NY"},
    IndexNo:  1,
}
```

`DB.Range()` then calls:

```go
EncodeKeyPrefix(schema, req.IndexNo, req.Start, ...)
EncodeKeyPrefix(schema, req.IndexNo, req.Stop, ...)
```

The important difference from earlier chapters is that the range boundaries
are encoded in the selected index's namespace, not always namespace zero.

---

## 3. Reading through a secondary index requires two lookups

The primary-index iterator can decode a complete row directly:

```text
primary KV entry
    ↓
decode primary key
decode value
    ↓
complete row
```

A secondary-index iterator cannot do that because its value is empty. It must
perform a second lookup:

```text
secondary KV entry
    ↓
decode (indexed columns + primary key)
    ↓
extract primary-key columns
    ↓
DB.Select(primary key)
    ↓
complete row
```

This is why `RowIterator` gains both a database pointer and an index number:

```go
type RowIterator struct {
    db      *DB
    schema  *Schema
    indexNo int
    iter    *RangedKVIter
    valid   bool
    row     Row
}
```

The iterator must know which decoding rules to use and must have access to the
database for the secondary-to-primary lookup.

### Primary-index decoding

When:

```go
iter.indexNo == 0
```

the iterator does the familiar work:

```go
row.DecodeKey(schema, 0, kvIter.Key())
row.DecodeVal(schema, kvIter.Val())
```

The value contains the non-primary columns, so the row is complete.

### Secondary-index decoding

When:

```go
iter.indexNo > 0
```

the iterator first decodes the secondary key:

```go
row.DecodeKey(schema, iter.indexNo, iter.iter.Key())
```

For the city index `{1, 0}`, this fills:

```text
row[1] = city
row[0] = primary id
```

Then it performs:

```go
ok, err := iter.db.Select(iter.schema, iter.row)
```

`Select` uses the primary-key columns already filled by `DecodeKey`, reads the
primary entry, and decodes the remaining values into the row.

If the secondary entry points to a missing primary row, the indexes are
inconsistent. The iterator should return an error rather than silently
returning a partial row.

---

## 4. Why `decodeKVIter` becomes a method

Earlier, decoding was a standalone helper:

```go
decodeKVIter(schema, iter, row)
```

That was sufficient while every iterator scanned the primary namespace.

Now decoding needs access to three pieces of iterator state:

```text
the database      -> required for DB.Select
the schema        -> required for key/value decoding
the index number  -> determines primary versus secondary behavior
```

Therefore it becomes a method:

```go
func (iter *RowIterator) decodeKVIter() (bool, error)
```

`Next()` advances the physical KV iterator, then asks the iterator itself to
decode its current entry:

```go
func (iter *RowIterator) Next() error {
    if err := iter.iter.Next(); err != nil {
        return err
    }

    iter.valid, err = iter.decodeKVIter()
    return err
}
```

This keeps the two-step behavior in one place:

```text
advance physical cursor
decode current entry according to indexNo
```

---

## 5. Index selection

A query condition is not automatically tied to the primary key anymore. The
database must test each index definition and find one whose ordered columns
match the condition.

The book separates the work into two functions:

```go
matchRangeByIndex(schema, indexNo, cond)
matchRange(schema, cond)
```

`matchRangeByIndex` asks:

> Can this condition be represented as a range over this particular index?

`matchRange` asks that question for every index:

```go
func matchRange(schema *Schema, cond interface{}) (*RangeReq, bool) {
    for indexNo := range schema.Indices {
        if req, ok := matchRangeByIndex(schema, indexNo, cond); ok {
            return req, true
        }
    }
    return nil, false
}
```

The first matching index wins. Since index 0 is the primary index, a query
that can use the primary key will normally select it before a secondary index.

### Example: primary-key query

```sql
WHERE id >= 10
```

The primary index is `{0}`, so the condition matches index 0:

```text
RangeReq.IndexNo = 0
```

The database scans primary keys directly and decodes complete rows from the
same entries.

### Example: secondary-index query

```sql
WHERE city >= 'LA'
```

The primary index `{0}` does not begin with `city`, so it does not match. The
secondary index `{1, 0}` begins with `city`, so it matches:

```text
RangeReq.IndexNo = 1
```

The database scans city-index entries and follows each embedded primary key
back to the primary row.

---

## 6. Ordered and composite-index matching

Indexes are ordered tuples, not unordered sets of columns.

For this index:

```go
schema.Indices[1] = []int{1, 0} // (city, id)
```

these conditions can use the index:

```sql
WHERE city = 'LA'
WHERE city >= 'LA'
WHERE city = 'LA' AND id >= 10
```

but this condition cannot efficiently use the `(city, id)` ordering by itself:

```sql
WHERE id >= 10
```

The first indexed column is `city`. A sorted `(city, id)` range is grouped by
city first; knowing only `id` does not identify one contiguous region.

This is the leftmost-prefix rule:

```text
index (A, B, C)

usable prefixes:
    A
    A, B
    A, B, C

not a leading prefix:
    B
    C
    B, C
```

The matcher must also preserve the order of bounds. For a condition joined by
`AND`, one comparison supplies the start boundary and the other supplies the
stop boundary:

```sql
city >= 'LA' AND city < 'NY'
```

becomes:

```text
start = LA, start comparison = >=
stop  = NY, stop comparison  = <
```

When porting the matcher, make sure the two sides of `AND` are inspected
separately:

```go
op1, cols1, cells1, ok := matchCmp(binop.left)
op2, cols2, cells2, ok := matchCmp(binop.right)
```

Using `binop.left` for both calls would silently ignore the upper (or lower)
bound on the right side. Check this carefully when comparing against the
reference solution.

If both conditions provide lower bounds, or both provide upper bounds, the
simple range representation cannot combine them and should reject that shape
as unsupported.

The implementation should normalize reversed comparisons too:

```sql
'LA' <= city
```

means the same thing as:

```sql
city >= 'LA'
```

---

## 7. Equality predicates

An equality condition is a small range whose start and stop are the same key:

```sql
WHERE id = 10
```

Conceptually:

```text
start >= 10
stop  <= 10
```

For a secondary equality query:

```sql
WHERE city = 'LA'
```

the selected index is the city index, and the range covers all keys beginning
with the city prefix:

```text
("LA", -infinity) through ("LA", +infinity)
```

The primary key appended to the secondary key is what allows multiple rows to
match the same city.

---

## 8. The mutation-while-iterating bug

0803 also fixes range updates and deletes.

The old pattern is unsafe:

```go
for iter.Valid() {
    row := iter.Row()
    db.Update(schema, row) // changes the same sorted structure
    iter.Next()
}
```

With secondary indexes, one row mutation can change multiple KV keys. The
sorted iterator may be backed by an array or slice. Inserting or deleting
entries while the iterator is positioned inside that array can:

```text
- shift the current position;
- skip the next row;
- visit a row twice; or
- make the cursor invalid.
```

The correct two-phase approach is:

```text
Phase 1: scan and copy all matching rows
Phase 2: update or delete the copied rows
```

For updates:

```go
oldRows := []Row{}

for ; err == nil && iter.Valid(); err = iter.Next() {
    oldRows = append(oldRows, slices.Clone(iter.Row()))
}

if err != nil {
    return 0, err
}

for _, row := range oldRows {
    // evaluate assignments and call db.Update
}
```

For deletes, use the same pattern:

```go
oldRows := []Row{}

for ; err == nil && iter.Valid(); err = iter.Next() {
    oldRows = append(oldRows, slices.Clone(iter.Row()))
}

if err != nil {
    return 0, err
}

for _, row := range oldRows {
    // call db.Delete
}
```

The `slices.Clone` is important. Storing `iter.Row()` directly may retain a
reference to the iterator's reusable row buffer. Later calls to `Next()` could
overwrite all previously collected rows.

---

## 9. Complete query execution flow

Consider:

```sql
SELECT id, name
FROM users
WHERE city = 'LA';
```

The execution flow is:

```text
SQL parser
    ↓
condition tree: city = "LA"
    ↓
matchRange()
    ↓
try index 0: primary id index does not match
    ↓
try index 1: city,id index matches
    ↓
RangeReq{IndexNo: 1, prefix: city="LA"}
    ↓
DB.Range()
    ↓
encode lower and upper keys in index-1 namespace
    ↓
scan secondary entries:
    (LA, 10), (LA, 30), (LA, 40)
    ↓
RowIterator.decodeKVIter()
    ↓
decode each secondary key and recover id
    ↓
DB.Select(id)
    ↓
decode complete primary rows
    ↓
apply SELECT projection: id, name
```

The secondary index narrows the search. The primary index remains the source
of the complete row data.

---

## 10. Complete range-update flow

Consider:

```sql
UPDATE users
SET name = 'Updated'
WHERE city = 'LA';
```

The safe execution order is:

```text
1. Select through the city index
2. Resolve each secondary entry to a complete row
3. Clone and save all matching rows in memory
4. Finish the iterator scan
5. For each saved row:
   a. evaluate the new values
   b. delete old primary and secondary entries
   c. insert new primary and secondary entries
```

The scan and mutation phases must not overlap.

---

## 11. Consistency behavior

An index entry can become inconsistent if it points to a missing primary row.
During secondary iteration:

```go
ok, err := db.Select(schema, row)
```

the iterator should treat:

```text
ok = false, err = nil
```

as an index-consistency error, because the secondary entry exists but its
primary target does not.

This is different from an ordinary query returning no rows. A valid range with
no matching entries simply has an invalid/exhausted iterator; a dangling index
entry indicates corrupted or partially maintained state.

Transactions in 0804 will eventually address atomicity across the multiple KV
writes involved in maintaining these entries.

---

## 12. Files changed for this chapter

When implementing 0803 from the 0802 code, the main work belongs in:

```text
table.go
    - RangeReq.IndexNo
    - RowIterator.db and RowIterator.indexNo
    - iterator decoding through primary or secondary indexes
    - Range() uses req.IndexNo
    - index selection in matchRange()
    - snapshot rows before range UPDATE/DELETE

table_test.go
    - secondary-index SELECT tests
    - composite-prefix/range tests
    - update/delete-through-secondary-index tests
```

`row.go` already has the index-aware key functions from 0802. The KV storage
engine does not need a new data structure for 0803; it already supports ranges
over the encoded key namespace.

The solution directory contains package-wide copies because each chapter is a
standalone Go package. Do not overwrite customized files blindly. Port the
behavior into your existing 0802-based code.

---

## 13. What this chapter does not solve

0803 does not provide:

```text
- transactions;
- atomic multi-key commits;
- concurrent query/update isolation;
- every possible SQL predicate;
- automatic selection among all theoretically possible indexes;
- covering-index payloads;
- full-text or spatial indexes.
```

It implements the first useful indexed-query path:

```text
ordered range predicate
    → matching index
    → range scan
    → primary-key recovery
    → complete row
```

---

## 14. Implementation checklist

Before considering 0803 complete, verify:

- `RangeReq` contains `IndexNo`.
- `RowIterator` stores `db` and `indexNo`.
- Primary-index iteration decodes the value directly.
- Secondary-index iteration decodes the secondary key and calls `DB.Select`.
- A missing primary row behind a secondary entry returns an inconsistency error.
- `Range()` encodes both boundaries with `req.IndexNo`.
- The matcher tries every index, not only index 0.
- Composite-index matching follows the leftmost-prefix rule.
- Range `AND` conditions produce one lower and one upper boundary.
- Range UPDATE collects cloned rows before mutating.
- Range DELETE collects cloned rows before mutating.
- Tests cover primary queries, secondary queries, duplicate secondary values,
  updates that change indexed columns, and deletes through an index.

The central lesson is:

> A secondary index finds candidate primary keys; the primary index remains the
> authoritative source of complete rows.
