# Chapter 0506: WHERE Expressions

## Overview

Chapter 0506 changes how the database represents a SQL WHERE clause.

Before this chapter, WHERE is parsed as a special list of primary-key
equalities:

```sql
WHERE id = 20 AND tenant = 'acme';
```

That becomes something like:

```text
keys = [
    id = 20,
    tenant = "acme",
]
```

This works for one narrow query shape, but it cannot preserve the structure of
conditions such as:

```sql
WHERE a + b > 10
WHERE active = 1 AND score >= 80
WHERE NOT deleted
```

The expression parser already knows how to build trees. This chapter reuses
that same machinery for WHERE:

```text
WHERE SQL text
      │
      ▼
  parseExpr()
      │
      ▼
condition expression tree
      │
      ▼
query matcher or planner
      │
      ├── current support: primary-key equality lookup
      └── future support: ranges and table scans
```

The central lesson is:

> Parsing a condition and choosing how to execute that condition are separate
> responsibilities.

The parser becomes general in this chapter, while the execution engine still
supports only one query plan: a single-row primary-key lookup.

---

## 1. Progression from Earlier Chapters

```text
0501  Parse infix arithmetic into expression trees
0502  Evaluate expression trees against a schema and row
0503  Add arithmetic precedence and parentheses
0504  Add comparisons, Boolean operators, and unary operators
0505  Use expression trees in SELECT and UPDATE
0506  Use an expression tree for WHERE and plan supported queries
```

Before 0506:

```text
SELECT expressions → expression trees
UPDATE expressions → expression trees
WHERE              → []NamedCell equality list
```

After 0506:

```text
SELECT expressions → expression trees
UPDATE expressions → expression trees
WHERE              → one condition expression tree
```

This makes all three statement types use the same kind of condition
representation.

---

## 2. Why a NamedCell List Is Not Enough

A NamedCell is useful for one already-understood equality:

```go
NamedCell{
    column: "id",
    value:  Cell(20),
}
```

However, a WHERE clause is not necessarily a list. It has operators, grouping,
precedence, and Boolean meaning.

For:

```sql
WHERE a = 1 OR b = 2;
```

the tree is:

```text
        OR
       /  \
    a=1   b=2
```

If the parser stored only a list containing a=1 and b=2, it would lose whether
the condition used OR or AND.

The new representation stores one expression tree:

```go
type StmtSelect struct {
    table string
    cols  []interface{}
    cond  interface{}
}
```

The cond field can contain a string, a Cell pointer, a binary-expression
pointer, or a unary-expression pointer.

---

## 3. New Statement Structures

The three statements that previously contained keys now contain cond:

```go
type StmtSelect struct {
    table string
    cols  []interface{}
    cond  interface{}
}

type StmtUpdate struct {
    table string
    cond  interface{}
    value []ExprAssign
}

type StmtDelete struct {
    table string
    cond  interface{}
}
```

The old keys fields are removed.

For:

```sql
WHERE id = 20 AND tenant = 'acme';
```

the condition becomes:

```text
             AND
            /   \
           =     =
          / \   / \
        id  20 tenant "acme"
```

The tree describes what the condition means. It is not yet a database key.

---

## 4. The New parseWhere Function

The old parser repeatedly called parseEqual:

```text
parse WHERE
    parse column = literal
    expect AND
    parse column = literal
    expect semicolon
```

The new parser calls parseExpr exactly once:

```text
find WHERE
    │
    ▼
parseExpr()
    │
    ▼
build the complete condition tree
    │
    ▼
require ;
```

The target shape is:

```go
func (p *Parser) parseWhere() (expr interface{}, err error) {
    if !p.tryKeyword("WHERE") {
        return nil, errors.New("expect keyword")
    }

    if expr, err = p.parseExpr(); err != nil {
        return nil, err
    }

    if !p.tryPunctuation(";") {
        return nil, errors.New("expect ;")
    }

    return expr, nil
}
```

For:

```sql
WHERE id = 20 AND tenant = 'acme';
```

parseExpr returns:

```text
        AND
       /   \
      =     =
     / \   / \
   id  20 tenant "acme"
```

The parser no longer needs a WHERE-specific loop for AND. The existing
parseAnd function already builds the Boolean nodes.

---

## 5. Updating the Statement Parsers

### SELECT

The SELECT parser already handles its expression list and table name. At the
end it should store the returned WHERE tree:

```text
parse selected expressions
parse FROM and table name
parse WHERE expression
store returned tree in out.cond
```

Conceptually:

```go
out.cond, err = p.parseWhere()
return err
```

For:

```sql
SELECT a + b FROM t WHERE id = 20;
```

the statement contains two separate trees:

```text
stmt.cols[0] = (a + b)
stmt.cond    = (id = 20)
```

### UPDATE

After parsing its SET assignments, UPDATE stores its WHERE tree in cond.
The old cursor rewind over WHERE is no longer needed because the new
parseWhere function consumes the keyword itself.

```text
parse table
parse SET assignment expressions
parse WHERE expression
store condition in out.cond
```

### DELETE

DELETE has no selected expressions or assignments. Its parser only needs:

```text
parse table name
parse WHERE expression
store tree in out.cond
```

---

## 6. Parsing and Planning Are Different

The condition tree is not automatically a storage key:

```text
condition tree
      │
      ▼
matcher or query planner
      │
      ├── primary-key point lookup
      ├── future range lookup
      └── future full-table scan
```

0506 implements only:

```text
AND-connected equality comparisons
where each comparison is column = literal
and the values identify one complete primary key
```

The parser can represent more than the planner can execute. That is
intentional.

For example, this is a valid expression tree:

```sql
WHERE a + b > 10;
```

But the current storage engine cannot turn it into one makePKey call.

---

## 7. matchAllEq

The chapter adds:

```go
func matchAllEq(cond interface{}, out []NamedCell) ([]NamedCell, bool)
```

Its question is:

> Is this entire condition an AND-connected list of direct
> column-equals-literal comparisons?

For:

```sql
WHERE id = 20 AND tenant = 'acme';
```

it extracts:

```text
keys = [
    id = 20,
    tenant = "acme",
]
true
```

For an unsupported condition it returns:

```text
nil, false
```

The Boolean does not mean that SQL evaluated to false. It means the condition
cannot be handled by the current key matcher.

---

## 8. How matchAllEq Recurses

The matcher recognizes two tree shapes.

### One equality

```text
    =
   / \
column literal
```

For id = 20, the matcher should:

```text
1. Confirm the node is an ExprBinOp.
2. Confirm the operator is OP_EQ.
3. Confirm the left child is a string column name.
4. Confirm the right child is a Cell literal.
5. Append a NamedCell.
6. Return success.
```

### AND of two supported conditions

```text
        AND
       /   \
 condition condition
```

For (id = 20) AND (tenant = "acme"):

```text
1. Recognize OP_AND.
2. Recursively match the left child.
3. Recursively match the right child.
4. Append both results to the same output list.
5. Succeed only when both sides succeed.
```

The all-or-nothing rule is:

```text
left supported AND right supported
          ↓
whole condition supported
```

---

## 9. Supported and Unsupported Shapes

Supported in this chapter:

```sql
WHERE id = 20;
WHERE id = 20 AND tenant = 'acme';
WHERE (id = 20) AND (tenant = 'acme');
```

Parentheses normally leave the same underlying tree shape because parseAtom
returns the expression inside them.

Unsupported for 0506:

```sql
WHERE id > 20;
WHERE id = 20 OR id = 30;
WHERE a + b = 10;
WHERE a = b;
WHERE NOT deleted;
```

These may be valid expressions, but they do not describe one complete
primary-key equality lookup. The range-query chapter adds more planning forms.
A future scan planner could evaluate arbitrary conditions row by row.

---

## 10. matchPKey

The matcher-to-storage bridge is:

```go
func matchPKey(schema *Schema, cond interface{}) (Row, error) {
    if keys, ok := matchAllEq(cond, nil); ok {
        return makePKey(schema, keys)
    }
    return nil, errors.New("unimplemented WHERE")
}
```

The responsibilities are separate:

```text
parseWhere()  → text to condition tree
matchAllEq()  → condition tree to NamedCell equality list
matchPKey()   → equality list to primary-key Row
db.Select()   → primary-key Row to stored Row
evalExpr()    → stored Row plus SELECT or UPDATE tree to Cell
```

matchPKey does not evaluate WHERE against an existing row. It extracts key
constraints before the row has been fetched.

---

## 11. SELECT Execution Flow

For:

```sql
SELECT a + b
FROM numbers
WHERE id = 1;
```

the flow is:

```text
1. Get the schema.
2. Read stmt.cond.
3. matchAllEq extracts id=1.
4. matchPKey creates the primary-key lookup row.
5. db.Select fetches the complete stored row.
6. evalExpr calculates a+b.
7. Return the SELECT result row.
```

The WHERE tree chooses the input row. The SELECT tree calculates the output:

```text
WHERE id = 1
      └── locate input row

SELECT a + b
      └── calculate output Cell from that row
```

---

## 12. UPDATE and DELETE Execution

UPDATE keeps the assignment behavior from 0505. Only its lookup source
changes:

```text
stmt.cond
   ↓
matchPKey()
   ↓
fetch original row
   ↓
evaluate all assignment expressions
   ↓
apply changes and write row
```

DELETE follows the same lookup path:

```text
stmt.cond
   ↓
matchPKey()
   ↓
delete matching primary-key row
```

All three statement types must stop using stmt.keys.

---

## 13. Complete Mock Example

Assume:

```text
schema columns:
    id       index 0, int64, primary key
    tenant   index 1, string, primary key
    a        index 2, int64
    b        index 3, int64
```

Query:

```sql
SELECT a + b
FROM accounts
WHERE id = 7 AND tenant = 'acme';
```

Parser output:

```text
StmtSelect
├── table: "accounts"
├── cols
│   └── (a + b)
└── cond
    └── AND
        ├── (= id 7)
        └── (= tenant "acme")
```

The matcher sees an AND root and recursively extracts:

```text
left equality  → id=7
right equality → tenant="acme"
```

It returns:

```text
[]NamedCell{
    {column: "id",     value: 7},
    {column: "tenant", value: "acme"},
}
```

makePKey creates:

```text
before: [empty, empty, empty, empty]
after:  [7, "acme", empty, empty]
```

Suppose the fetched row is:

```text
[7, "acme", 12, 5]
```

SELECT then evaluates:

```text
a + b = 12 + 5 = 17
```

and returns:

```text
[17]
```

The WHERE expression found the row. The SELECT expression calculated the
result.

---

## 14. Why the Matcher Returns bool

The matcher returns a list and a Boolean:

```go
func matchAllEq(cond interface{}, out []NamedCell) ([]NamedCell, bool)
```

The Boolean answers:

```text
Can this condition be represented by the current primary-key lookup plan?
```

It does not answer whether a particular fetched row satisfies the condition.
For example, id > 20 might be true for a row, but the matcher still returns
false because ranges are not implemented yet.

---

## 15. Planner Limitations

One logical condition can have multiple equivalent forms. For example:

```text
(a, b) > (1, 2)
```

can be written conceptually as:

```text
a > 1 OR (a = 1 AND b > 2)
```

A small matcher may recognize one form and fail to recognize another. This is
normal while a query planner is being developed.

Indexes create another constraint. If an index is ordered by (a,b), the
planner must recognize a WHERE shape that can use that ordering.

The practical lesson is:

> A valid expression tree does not guarantee that every query plan can execute
> it.

0506 introduces this boundary by returning unimplemented WHERE for shapes
outside its current matcher.

---

## 16. File-by-File Implementation Guide

### sql_parser.go

Change:

```text
StmtSelect.keys  → StmtSelect.cond
StmtUpdate.keys  → StmtUpdate.cond
StmtDelete.keys  → StmtDelete.cond
```

Replace the slice-oriented WHERE parser with:

```text
parseWhere() (interface{}, error)
```

Update all three statement parsers to store the returned tree in cond.

### table.go

Add matchAllEq and matchPKey. Replace direct makePKey calls that use
stmt.keys with matchPKey using stmt.cond.

The rest of SELECT, UPDATE, and DELETE can continue using the resulting row.

### Tests

Parser tests should log SQL input, condition trees, prefix form, and cursor
completion.

Matcher tests should cover one equality, AND-connected equalities, parentheses,
OR rejection, range rejection, and computed-equality rejection.

Execution tests should prove that SELECT, UPDATE, and DELETE still locate a
primary-key row through cond.

---

## 17. Recommended Implementation Order

```text
1. Change the three statement structs from keys to cond.
2. Rewrite parseWhere to return parseExpr output.
3. Update parseSelect, parseUpdate, and parseDelete.
4. Add parser tests and inspect condition trees.
5. Implement matchAllEq for one equality.
6. Extend matchAllEq recursively for AND.
7. Implement matchPKey.
8. Replace execution calls with matchPKey.
9. Add matcher and execution tests.
10. Run formatting and the complete 0506 suite.
```

Useful commands:

```bash
gofmt -w 0506/*.go
GOCACHE=/tmp/db-engine-go-cache go test -v ./0506
```

---

## 18. Common Mistakes

### Keeping a WHERE slice

Do not keep parsing WHERE into []NamedCell. That loses Boolean structure.
Store one cond expression tree.

### Evaluating WHERE during parsing

The parser does not have a fetched row. It builds a tree. Matching or
row-by-row evaluation happens later.

### Calling evalExpr to build a primary key

evalExpr resolves names using an existing row. matchAllEq extracts literal key
constraints before a row exists.

### Treating matcher false as SQL false

matchAllEq returning false means an unsupported query plan, not a false SQL
condition.

### Forgetting DELETE

SELECT, UPDATE, and DELETE all used keys before 0506. All three now need cond
and matchPKey.

### Leaving the old UPDATE rewind

The old parser backed up over WHERE. The new parseWhere consumes WHERE itself,
so that cursor rewind should be removed.

### Accepting every equality

The current matcher accepts only:

```text
direct column = literal
```

It does not yet accept:

```text
a + 1 = 10
a = b
```

---

## Final Mental Model

```text
WHERE text
   │
   ▼
parseExpr()
   │
   ▼
general condition tree
   │
   ▼
matchAllEq()
   │
   ├── supported equality tree
   │       │
   │       ▼
   │   NamedCell key list
   │       │
   │       ▼
   │   makePKey() + point lookup
   │
   └── unsupported tree
           │
           ▼
       unimplemented WHERE
```

The shortest summary is:

> 0505 taught SELECT and UPDATE to use expression trees. 0506 teaches WHERE
> to become an expression tree, then recognizes the primary-key equality trees
> that the current storage engine can execute.

---

## Chapter Completion Checklist

- [ ] StmtSelect stores cond interface{} instead of keys.
- [ ] StmtUpdate stores cond interface{} instead of keys.
- [ ] StmtDelete stores cond interface{} instead of keys.
- [ ] parseWhere returns one expression tree.
- [ ] parseSelect stores its WHERE tree in cond.
- [ ] parseUpdate stores its WHERE tree in cond.
- [ ] parseDelete stores its WHERE tree in cond.
- [ ] matchAllEq recognizes one column-literal equality.
- [ ] matchAllEq recursively recognizes AND-connected equalities.
- [ ] matchPKey converts supported conditions into a lookup row.
- [ ] SELECT uses matchPKey.
- [ ] UPDATE uses matchPKey.
- [ ] DELETE uses matchPKey.
- [ ] Unsupported WHERE shapes return a clear error.
- [ ] Parser tests log condition trees.
- [ ] Matcher tests cover supported and unsupported shapes.
- [ ] Existing SELECT, UPDATE, and DELETE behavior still passes.
- [ ] gofmt and go test -v ./0506 pass.
