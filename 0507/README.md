# Chapter 0507: SQL Range Queries

## Overview

Chapter 0507 connects SQL WHERE conditions to the range-scan machinery that
already exists in the storage engine.

Before this chapter, 0506 can recognize a primary-key equality condition:

```sql
WHERE id = 123;
```

The condition is converted into one lookup row and sent to DB.Select.

0507 adds conditions such as:

```sql
SELECT a, b FROM t WHERE a > 123;
SELECT a, b FROM t WHERE (a, b) > (123, 0);
```

These conditions do not identify one exact row. They identify an ordered
section of the database. The engine must use DB.Range and a RowIterator.

The new pipeline is:

```text
WHERE expression
      │
      ▼
  parseExpr()
      │
      ▼
condition expression tree
      │
      ▼
  makeRange()
      │
      ▼
RangeReq
      │
      ▼
DB.Range()
      │
      ▼
RowIterator
```

The central lesson is:

> A point lookup asks for one exact key. A range query describes a start
> boundary, a stop boundary, and how each boundary is compared.

---

## 1. Progression from Earlier Chapters

```text
0501  Parse arithmetic expressions
0502  Evaluate expression trees
0503  Add precedence and parentheses
0504  Add comparison and Boolean expression parsing
0505  Use expressions in SELECT and UPDATE
0506  Turn WHERE into a condition tree and match primary-key equalities
0507  Turn supported WHERE conditions into range scans
```

0506 uses this path:

```text
condition tree
      ↓
matchAllEq()
      ↓
NamedCell equality list
      ↓
makePKey()
      ↓
DB.Select()
      ↓
one row
```

0507 changes it to:

```text
condition tree
      ↓
makeRange()
      ↓
RangeReq
      ↓
DB.Range()
      ↓
RowIterator
      ↓
zero or more rows
```

An equality query can also be represented as a range whose start and stop are
the same key.

---

## 2. The Existing Range API

The storage layer already defines:

```go
type RangeReq struct {
    StartCmp ExprOp
    StopCmp  ExprOp
    Start    []Cell
    Stop     []Cell
}
```

The fields mean:

| Field | Meaning |
|---|---|
| StartCmp | Comparison used at the lower boundary |
| StopCmp | Comparison used at the upper boundary |
| Start | Lower boundary key components |
| Stop | Upper boundary key components |

The storage method is:

```go
func (db *DB) Range(
    schema *Schema,
    req *RangeReq,
) (*RowIterator, error)
```

The iterator exposes:

```go
iter.Valid()
iter.Row()
iter.Next()
```

The normal iteration pattern is:

```text
create iterator
while iterator is valid:
    read current row
    move to next row
```

---

## 3. Why Execution Must Change

The old execution functions assume one row:

```text
build one key row
DB.Select()
process one row
```

A range can match many rows:

```text
build RangeReq
DB.Range()
iterate over matching rows
process every row
```

Therefore all three operations need range-aware execution:

```text
SELECT → evaluate expressions for every iterator row
UPDATE → evaluate and save every matching row
DELETE → delete every matching row
```

The book introduces a shared helper:

```go
func (db *DB) execCond(
    schema *Schema,
    cond interface{},
) (*RowIterator, error) {
    req, err := makeRange(schema, cond)
    if err != nil {
        return nil, err
    }
    return db.Range(schema, req)
}
```

This keeps the condition-to-iterator logic in one place.

---

## 4. Range Boundaries and Strictness

For a lower boundary:

```text
id >= 20 → StartCmp is OP_GE
id >  20 → StartCmp is OP_GT
```

For an upper boundary:

```text
id <= 40 → StopCmp is OP_LE
id <  40 → StopCmp is OP_LT
```

For:

```sql
WHERE id > 20 AND id <= 40;
```

the request is conceptually:

```text
Start    = 20
StartCmp = OP_GT
Stop     = 40
StopCmp  = OP_LE
```

The result includes values greater than 20 and includes 40.

The existing key-prefix and infinity logic supplies the physical boundaries
needed by the storage engine. The planner must preserve the comparison
strictness while constructing the request.

---

## 5. Composite-Key Tuples

0507 supports syntax such as:

```sql
WHERE (a, b) > (123, 0);
```

The parser needs a node for an ordered list of children:

```go
type ExprTuple struct {
    kids []interface{}
}
```

This differs from a binary node:

```text
ExprBinOp → one operator with left and right children
ExprTuple → an ordered list of child expressions
```

Tuple order matters:

```text
(a, b) is not the same ordered key as (b, a)
```

The left tuple normally contains column expressions:

```text
(a, b)
 ├── a
 └── b
```

The right tuple normally contains literal values:

```text
(123, 0)
 ├── 123
 └── 0
```

---

## 6. Parenthesis Parsing

Before tuples, parentheses mean grouping:

```text
(a + b)
    ↓
return the inner a+b expression tree
```

Tuples need to distinguish:

```text
(a + b) → grouped expression
(a, b) → tuple
```

The book changes parseAtom so it can send parenthesized input to parseTuple.
The tuple parser should:

```text
1. Consume the opening parenthesis.
2. Parse the first expression.
3. If a comma follows, parse more expressions.
4. Require the closing parenthesis.
5. Return an ExprTuple.
```

The existing grouping behavior must continue to work.

---

## 7. Equality as a Range

The equality:

```sql
WHERE id = 123;
```

is equivalent to:

```text
id >= 123 AND id <= 123
```

Therefore makeRange first tries the equality matcher:

```go
if keys, ok := matchAllEq(cond, nil); ok {
    if pkey, ok := extractPKey(schema, keys); ok {
        return &RangeReq{
            StartCmp: OP_GE,
            StopCmp:  OP_LE,
            Start:    pkey,
            Stop:     pkey,
        }, nil
    }
}
```

An equality condition becomes a range with identical start and stop keys.
That range returns at most one row.

---

## 8. Primary-Key Ordering

The equality matcher returns NamedCell values in expression order. The schema
knows the physical primary-key order.

Example:

```text
matched equalities:
    tenant = "acme"
    id = 7

schema primary-key order:
    id, tenant

ordered key:
    [7, "acme"]
```

extractPKey must reorder the values according to schema.PKey. It must also
reject incomplete primary keys.

This matters because the encoded key must use the same order as the stored
composite key.

---

## 9. Matching Range Conditions

When equality matching does not apply, makeRange calls matchRange:

```go
req, ok := matchRange(schema, cond)
```

The first range shape is a direct comparison:

```text
column > literal
column >= literal
column < literal
column <= literal
```

For:

```sql
WHERE id > 123;
```

the matcher extracts:

```text
column  = id
operator = OP_GT
value   = 123
```

It then creates one finite boundary and one infinite boundary.

The second shape is an AND of two comparisons:

```sql
WHERE id > 123 AND id <= 200;
```

The matcher combines:

```text
lower comparison → Start and StartCmp
upper comparison → Stop and StopCmp
```

Only one range is supported in this chapter. OR and multiple range unions are
deferred.

---

## 10. makeRange

The main planner function is:

```go
func makeRange(
    schema *Schema,
    cond interface{},
) (*RangeReq, error)
```

Its decision order is:

```text
1. Try to recognize complete primary-key equality.
2. If that fails, try to recognize one range condition.
3. If neither works, return unimplemented WHERE.
```

Conceptually:

```go
if equality condition:
    extract ordered primary key
    return equal start and stop RangeReq

if range condition:
    return its RangeReq

return error
```

This keeps 0506 behavior available while adding ranges.

---

## 11. execCond

The shared execution helper is:

```go
func (db *DB) execCond(
    schema *Schema,
    cond interface{},
) (*RowIterator, error) {
    req, err := makeRange(schema, cond)
    if err != nil {
        return nil, err
    }
    return db.Range(schema, req)
}
```

Its responsibilities are:

```text
1. Convert the condition tree to RangeReq.
2. Return an error for unsupported conditions.
3. Ask DB.Range for an iterator.
```

It does not evaluate SELECT expressions or UPDATE assignments.

---

## 12. SELECT Iteration

The old SELECT path handles one row:

```text
build key row
DB.Select()
evaluate SELECT expressions
return one row
```

The new path handles an iterator:

```text
iter, err := db.execCond(&schema, stmt.cond)

for ; err == nil && iter.Valid(); err = iter.Next() {
    row := iter.Row()
    evaluate SELECT expressions
    append output row
}
```

Expressions must be evaluated inside the loop because iter.Row changes on every
iteration.

---

## 13. UPDATE and DELETE Iteration

UPDATE processes every matching row:

```text
create iterator
for every valid row:
    evaluate all assignments against the current original row
    apply updates
    write row
    move iterator forward
```

The 0505 simultaneous-assignment rule still applies to each individual row.
All right-hand expressions for that row must be evaluated before mutation.

DELETE uses two phases because the KV iterator references the same sorted
slices that deletion modifies:

```text
create iterator
for every valid row:
    copy and collect the row

after iteration finishes:
    delete every collected row
```

Deleting during iteration would shift the remaining keys to lower indexes and
could skip a row. Collecting first keeps iteration stable. Multiple-row tests
are essential.

---

## 14. Complete Mock Example

Assume:

```text
schema:
    id  index 0, int64, primary key
    a   index 1, int64
    b   index 2, int64
```

Stored rows:

```text
[10, 100, 1]
[20, 200, 2]
[30, 300, 3]
[40, 400, 4]
```

Query:

```sql
SELECT a, b FROM t WHERE id > 20 AND id <= 40;
```

The condition tree is:

```text
          AND
         /   \
        >     <=
       / \   /  \
     id 20 id  40
```

makeRange creates:

```text
Start    = [20]
StartCmp = OP_GT
Stop     = [40]
StopCmp  = OP_LE
```

DB.Range returns:

```text
[30, 300, 3]
[40, 400, 4]
```

SELECT evaluates each row:

```text
[30, 300, 3] → [300, 3]
[40, 400, 4] → [400, 4]
```

Final result:

```text
[[300, 3], [400, 4]]
```

The WHERE tree chooses rows. The SELECT expressions produce output cells.

---

## 15. Scope of This Chapter

Supported goals:

```text
one equality point lookup through the range path
one lower bound
one upper bound
one lower and one upper bound
one composite tuple range
SELECT, UPDATE, and DELETE over matching rows
```

Not yet supported:

```text
OR producing multiple ranges
range unions and intersections
general full-table scans
advanced index selection
```

The parser and planner remain intentionally smaller than full SQL.

---

## 16. File-by-File Guide

### sql_parser.go

Add ExprTuple and parseTuple. Update parenthesis handling so grouped
expressions and comma-separated tuples are both supported.

### table.go

Add:

```text
extractPKey
matchRange
makeRange
execCond
```

Then change SELECT, UPDATE, and DELETE to consume RowIterator values.

### Tests

Parser tests should cover:

```text
(a, b)
(a + 1, b * 2)
tuple comparisons
malformed tuples
```

Planner tests should cover:

```text
equality
lower bounds
upper bounds
two-sided ranges
strict and inclusive boundaries
composite tuple ranges
unsupported OR
```

Execution tests should log:

```text
condition tree
RangeReq
iterator rows
SELECT output
UPDATE effects
DELETE effects
```

---

## 17. Implementation Order Used

```text
1. Add ExprTuple.
2. Update parseAtom and implement parseTuple.
3. Add tuple parser tests.
4. Implement extractPKey.
5. Implement matchRange for one comparison.
6. Extend matchRange for an AND range.
7. Implement makeRange.
8. Implement execCond.
9. Convert execSelect to iterate.
10. Convert execUpdate and execDelete to iterate.
11. Add multi-row execution tests.
12. Run the complete 0507 test suite.
```

Useful commands:

```bash
gofmt -w 0507/*.go
GOCACHE=/tmp/db-engine-go-cache go test -v ./0507
```

---

## 18. Common Mistakes

### Treating a range like one key

id > 20 cannot be passed to makePKey. It needs start and stop boundaries.

### Losing strictness

Do not treat greater-than and greater-than-or-equal as equivalent.

### Forgetting tuple order

(a, b) and (b, a) describe different ordered keys.

### Processing only the first iterator row

Range queries can return many rows. Every valid iterator position must be
processed.

### Mixing parser and planner responsibilities

parseTuple builds a tuple tree. makeRange decides how that tree maps to a
storage range.

### Supporting OR too early

OR may require multiple range calls and range-union logic. This chapter stops
before that complexity.

---

## Final Mental Model

```text
WHERE expression
      │
      ▼
expression tree, possibly containing ExprTuple
      │
      ▼
makeRange()
      │
      ├── equality → same start and stop
      ├── lower bound → start plus infinite stop
      ├── upper bound → infinite start plus stop
      └── two-sided AND → start and stop
      │
      ▼
RangeReq
      │
      ▼
DB.Range()
      │
      ▼
RowIterator
      │
      ├── SELECT evaluates output expressions
      ├── UPDATE evaluates and writes each row
      └── DELETE removes each row
```

The shortest summary is:

> 0506 turned WHERE into a condition tree. 0507 turns supported condition
> trees into ordered ranges and teaches SQL execution to process every row
> returned by the range iterator.

---

## Chapter Completion Checklist

- [x] ExprTuple represents comma-separated expressions.
- [x] parseAtom and parseTuple parse tuple syntax.
- [x] Tuple parser tests pass.
- [x] extractPKey orders equality values by the schema primary key.
- [x] matchRange recognizes a single comparison.
- [x] matchRange recognizes a two-sided AND range.
- [x] Strict and inclusive boundaries are preserved.
- [x] makeRange handles equality and range conditions.
- [x] execCond creates a RowIterator.
- [x] SELECT iterates over all matching rows.
- [x] UPDATE iterates over all matching rows.
- [x] DELETE safely processes all matching rows in two phases.
- [x] Composite tuple ranges are tested.
- [x] Reversed comparisons such as `20 < id` are normalized.
- [x] Unsupported OR and complex plans return clear errors.
- [x] Tests log trees, RangeReq values, iterator rows, and results.
- [x] gofmt, go test, and go vet pass for `./0507`.
