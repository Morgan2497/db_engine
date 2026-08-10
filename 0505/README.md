# Chapter 0505: SELECT Expressions

## Overview

Chapter 0505 connects the expression system from the previous chapters to
real SQL statements.

The progression so far is:

```text
0501  Parse + and - expressions into trees
0502  Evaluate expression trees against a row
0503  Add precedence, *, /, and parentheses
0504  Add comparisons, Boolean operators, and unary operators
0505  Use expression trees in SELECT and UPDATE
```

This chapter is mostly integration. It does not introduce another expression
parsing algorithm. Instead, it changes `SELECT` and `UPDATE` so they can store
the expression trees that `parseExpr()` already builds and execute those trees
with `evalExpr()`.

The central idea is:

> Parsing builds an expression tree first. After a row has been fetched,
> execution evaluates that tree using the values in the row.

Before this chapter, the engine accepts only simple column names in a SELECT
list:

```sql
SELECT a, b FROM t WHERE id = 1;
```

After this chapter, each selected item can be a complete expression:

```sql
SELECT a * 4 - b, d + c FROM t WHERE id = 1;
```

Updates gain the same ability:

```sql
UPDATE t
SET a = a - b,
    b = a,
    c = d + c
WHERE id = 1;
```

---

## 1. The Complete Pipeline

The expression parser and evaluator used to exist as separate pieces:

```text
SQL expression text
        │
        ▼
   parseExpr()
        │
        ▼
 expression tree

expression tree + schema + row
        │
        ▼
    evalExpr()
        │
        ▼
    result Cell
```

Chapter 0505 joins those pieces inside SQL execution:

```text
SQL statement
      │
      ▼
parseSelect() or parseUpdate()
      │
      ▼
statement containing expression trees
      │
      ▼
fetch the matching row from the database
      │
      ▼
evalExpr(schema, row, expression)
      │
      ▼
return the calculated SELECT row
or write the calculated UPDATE values
```

Parsing and evaluation still happen at different times.

```text
Parsing:    What does this expression mean structurally?
Evaluation: What value does it produce for this particular row?
```

---

## 2. Why `StmtSelect.cols` Must Change

Before 0505, `StmtSelect` stores only column names:

```go
type StmtSelect struct {
    table string
    cols  []string
    keys  []NamedCell
}
```

For this query:

```sql
SELECT a, b FROM t WHERE id = 1;
```

the parser stores:

```text
cols[0] = "a"
cols[1] = "b"
```

But a `string` slice cannot store a binary-expression node such as:

```text
        -
       / \
      *   b
     / \
    a   4
```

The target structure therefore becomes:

```go
type StmtSelect struct {
    table string
    cols  []interface{}
    keys  []NamedCell
}
```

Each element of `cols` can now contain any expression-node type:

| Go value | Expression meaning |
|---|---|
| `string` | Column reference such as `a` |
| `*Cell` | Literal such as `4` or `"Sales"` |
| `*ExprUnOp` | Unary expression such as `-a` or `NOT a` |
| `*ExprBinOp` | Binary expression such as `a + b` |

`interface{}` does not mean that the value has no type. The value still has a
concrete runtime type. It means that one field can hold several different
kinds of expression node.

---

## 3. Parsing a SELECT Expression List

Previously, `parseSelect()` used `tryName()` for every selected item:

```text
tryName() can parse:  a
tryName() cannot parse all of:  a * 4 - b
```

Chapter 0505 replaces that column-name parsing with `parseExpr()`.

Conceptually, the loop becomes:

```text
until FROM is reached:
    if this is not the first expression:
        require a comma

    parse one complete expression
    append its tree to stmt.cols
```

Consider:

```sql
SELECT a * 4 - b, d + c FROM t WHERE id = 1;
```

The first call to `parseExpr()` begins at `a` and produces:

```text
        -
       / \
      *   b
     / \
    a   4
```

It stops at the comma because the comma is not an expression operator.

After the comma, the second call produces:

```text
    +
   / \
  d   c
```

It stops at `FROM` because `FROM` is not part of that expression.

The completed statement is conceptually:

```text
StmtSelect
├── table: "t"
├── cols
│   ├── ((a * 4) - b)
│   └── (d + c)
└── keys
    └── id = 1
```

The parser stores these trees. It does not calculate their values.

---

## 4. Executing SELECT Expressions

Before this chapter, `execSelect()` follows this model:

```text
selected column names
        │
        ▼
look up their numeric indexes
        │
        ▼
fetch the complete row
        │
        ▼
copy cells at those indexes
```

That works for `SELECT a, b`, but it cannot produce `a + b`. There is no
stored column whose index represents `a + b`.

The new model is:

```text
selected expression trees
        │
        ▼
fetch the complete row
        │
        ▼
evaluate every expression against that row
        │
        ▼
place each resulting Cell in the output row
```

The important loop is conceptually:

```go
out := make(Row, len(stmt.cols))

for i, expr := range stmt.cols {
    cell, err := evalExpr(&schema, row, expr)
    if err != nil {
        return nil, err
    }
    out[i] = *cell
}
```

### Concrete execution example

Query:

```sql
SELECT a * 4 - b, d + c FROM t WHERE id = 1;
```

Fetched row:

```text
id = 1
a  = 10
b  = 3
c  = 7
d  = 2
```

First selected expression:

```text
((a * 4) - b)
       │
       ├── a → 10
       ├── 4 → 4
       ├── 10 * 4 → 40
       ├── b → 3
       └── 40 - 3 → 37
```

Second selected expression:

```text
(d + c)
   │
   ├── d → 2
   ├── c → 7
   └── 2 + 7 → 9
```

The output row is:

```text
[37, 9]
```

The original stored row is not modified by a SELECT.

---

## 5. Why UPDATE Needs a New Structure

Before 0505, the right side of an assignment is parsed directly as a value:

```sql
UPDATE t SET a = 10 WHERE id = 1;
```

That can be represented by `NamedCell`:

```text
column = "a"
value  = Cell(10)
```

But consider:

```sql
UPDATE t SET a = a - b WHERE id = 1;
```

The right side has no final value during parsing. Its result depends on the
row that will be fetched later.

For one row it may produce:

```text
a = 10, b = 3  →  a - b = 7
```

For a different row it may produce:

```text
a = 50, b = 8  →  a - b = 42
```

Therefore an UPDATE assignment initially needs to store the expression tree,
not an evaluated `Cell`.

The new structure is:

```go
type ExprAssign struct {
    column string
    expr   interface{}
}
```

For:

```sql
a = a - b
```

it stores:

```text
ExprAssign
├── column: "a"
└── expr
    └── SUB
        ├── "a"
        └── "b"
```

`ExprAssign` and `NamedCell` represent two different stages:

| Structure | Stage | Right-hand side contains |
|---|---|---|
| `ExprAssign` | Parsed but not evaluated | Expression tree |
| `NamedCell` | Evaluated and ready to apply | Final `Cell` value |

The target `StmtUpdate` is:

```go
type StmtUpdate struct {
    table string
    keys  []NamedCell
    value []ExprAssign
}
```

---

## 6. Parsing an Assignment

The new `parseAssign()` function parses one item from the `SET` list.

For:

```sql
a = a - b
```

its responsibilities are:

```text
1. Parse destination column "a"
2. Require the = punctuation
3. Parse the complete expression "a - b"
4. Store the column and expression tree in ExprAssign
```

Its shape is:

```go
func (p *Parser) parseAssign(out *ExprAssign) (err error) {
    var ok bool

    out.column, ok = p.tryName()
    if !ok {
        return errors.New("expect column")
    }

    if !p.tryPunctuation("=") {
        return errors.New("expect =")
    }

    out.expr, err = p.parseExpr()
    return err
}
```

Compare the old and new parsing operations:

```text
parseEqual():
    column = parse name
    value  = parseValue()

parseAssign():
    column = parse name
    expr   = parseExpr()
```

`parseValue()` accepts one literal value. `parseExpr()` can accept a literal,
a column reference, a unary operation, a binary operation, or a parenthesized
combination of them.

---

## 7. Parsing the UPDATE Assignment List

Consider:

```sql
UPDATE t
SET a = a - b,
    b = a,
    c = d + c
WHERE id = 1;
```

`parseUpdate()` calls `parseAssign()` three times and builds:

```text
StmtUpdate
├── table: "t"
├── value
│   ├── ExprAssign
│   │   ├── column: "a"
│   │   └── expr: (a - b)
│   ├── ExprAssign
│   │   ├── column: "b"
│   │   └── expr: a
│   └── ExprAssign
│       ├── column: "c"
│       └── expr: (d + c)
└── keys
    └── id = 1
```

The commas separate assignments. They are not part of the expression trees.
The `WHERE` keyword ends the assignment list.

---

## 8. Executing UPDATE Expressions

`execUpdate()` must perform these stages in order:

```text
1. Get the table schema
2. Construct the primary key from WHERE
3. Fetch the original row
4. Evaluate every assignment expression against the original row
5. Convert the results into NamedCell values
6. Apply those values to the row
7. Write the completed row back to storage
```

The conversion stage is conceptually:

```go
updates := make([]NamedCell, len(stmt.value))

for i, assign := range stmt.value {
    cell, err := evalExpr(&schema, row, assign.expr)
    if err != nil {
        return 0, err
    }

    updates[i] = NamedCell{
        column: assign.column,
        value:  *cell,
    }
}
```

After the loop, the existing row-filling code can apply `updates`.

### Why evaluation must finish before mutation

This is the most important UPDATE rule in this chapter.

Suppose the original row is:

```text
a = 10
b = 3
c = 2
d = 1
```

The update is:

```sql
SET a = a - b,
    b = a,
    c = d + c
```

All right-hand expressions must read from the same original row:

```text
new a = old a - old b = 10 - 3 = 7
new b = old a         = 10
new c = old d + old c = 1 + 2 = 3
```

Only after calculating all three results should they be applied:

```text
original row: a=10, b=3,  c=2, d=1
updated row:  a=7,  b=10, c=3, d=1
```

If the code changes `a` immediately and then evaluates `b = a`, it produces:

```text
a = 7
b = 7    ← wrong; this read the already-modified a
```

The safe mental model is:

```text
READ PHASE                      WRITE PHASE

evaluate a = a - b ─┐
evaluate b = a     ──┼──► collect results ──► modify row once
evaluate c = d + c ─┘
```

---

## 9. SELECT and UPDATE Use the Same Evaluator

The expressions are not special to either statement.

```text
SELECT expression
        └── evalExpr(schema, fetchedRow, expr)

UPDATE assignment expression
        └── evalExpr(schema, fetchedRow, expr)
```

This reuse is the architectural lesson of the chapter. The expression parser
does not need to know where its tree will be used, and the evaluator does not
need to know which SQL statement supplied the tree.

```text
parseExpr() creates a reusable representation
evalExpr() gives that representation a value for a row
SELECT and UPDATE decide what to do with the value
```

---

## 10. SELECT Headers Need Special Attention

The current result type stores headers as strings:

```go
type SQLResult struct {
    Header []string
    // ...
}
```

Before 0505, this works directly:

```go
r.Header = stmt.cols
```

because `stmt.cols` is `[]string`.

After `stmt.cols` becomes `[]interface{}`, that assignment no longer compiles.
An expression tree cannot be directly assigned to a string slice.

Eventually the engine needs a policy for expression labels:

```text
expression       possible result header
a                a
a + b            a + b
a * 4 - b        a * 4 - b
```

The chapter's main concern is expression parsing and evaluation, so header
formatting is a small integration detail. Preserve `Header` as `[]string` and
convert each selected expression into an appropriate display label instead of
changing the public result header to `[]interface{}`.

---

## 11. What Does Not Change in 0505

This chapter deliberately has a narrow scope.

The following parts do not become general expressions yet:

- `WHERE` still uses `[]NamedCell` and primary-key equality.
- `INSERT` still receives literal values.
- `DELETE` parsing does not change.
- The expression precedence algorithm does not change.
- No new arithmetic or Boolean operators are introduced.
- Expressions are not evaluated during parsing.

In particular, do not change this chapter's WHERE clause into:

```sql
WHERE a + b > 10
```

General WHERE expressions belong to the next chapter.

---

## 12. File-by-File Guide

### `sql_parser.go`

The parser-side changes are:

```text
StmtSelect.cols       []string → []interface{}
add ExprAssign        destination column + expression tree
StmtUpdate.value      []NamedCell → []ExprAssign
parseSelect()         tryName() → parseExpr()
add parseAssign()     parse name, =, and expression
parseUpdate()         parseEqual() → parseAssign()
```

### `table.go`

The execution-side changes are:

```text
execSelect()
    fetch full row
    evaluate every selected expression
    return the calculated output row

execUpdate()
    fetch original row
    evaluate every assignment
    collect NamedCell results
    apply results only after evaluation completes
    write updated row

ExecStmt()
    convert SELECT expressions into string headers
```

### `exprEval.go`

The evaluator should already know how to evaluate the expression trees from
0504. This chapter primarily reuses it. Changes are needed only if tests reveal
an operator case that the evaluator does not yet support.

### Test files

Add parser tests and execution tests for SELECT and UPDATE expressions. The
tests should log the parsed tree, input row, evaluation order, and result so
the entire process remains visible while learning.

---

## 13. Recommended Implementation Order

Work in small, compileable stages:

```text
1. Change StmtSelect.cols to []interface{}
2. Add ExprAssign
3. Change StmtUpdate.value to []ExprAssign
4. Update parseSelect() to call parseExpr()
5. Implement parseAssign()
6. Update parseUpdate() to call parseAssign()
7. Add parser tests
8. Update execSelect()
9. Update execUpdate()
10. Resolve SELECT header conversion
11. Add execution tests
12. Run formatting and all tests
```

After each small change, run:

```bash
gofmt -w 0505/*.go
go test -v ./0505
```

If the terminal is already inside the `0505` directory, use:

```bash
gofmt -w *.go
go test -v
```

---

## 14. Essential Tests

### SELECT parser test

Input:

```sql
SELECT a * 4 - b, d + c FROM t WHERE id = 1;
```

Expected expression grouping:

```text
((a * 4) - b)
(d + c)
```

### SELECT execution test

Given:

```text
a=10, b=3, c=7, d=2
```

Expected result:

```text
[37, 9]
```

### UPDATE parser test

Input:

```sql
UPDATE t SET a = a - b, b = a, c = d + c WHERE id = 1;
```

Expected assignment trees:

```text
a ← (a - b)
b ← a
c ← (d + c)
```

### UPDATE execution test

Given:

```text
a=10, b=3, c=2, d=1
```

Expected result:

```text
a=7, b=10, c=3, d=1
```

The `b=10` assertion proves that every right-hand expression was evaluated
against the original row.

### Error tests

Useful failures to test include:

```text
unknown column in a SELECT expression
unknown column in an UPDATE expression
missing expression after a comma
missing = in an assignment
type mismatch during expression evaluation
unsupported operation for a value type
```

---

## 15. Common Mistakes

### Calling `parseValue()` for an assignment

Wrong idea:

```text
a = a - b
    └───── parseValue() cannot parse this complete expression
```

Use `parseExpr()` for the right-hand side.

### Evaluating during parsing

The parser does not have the row containing `a` and `b`, so it cannot know the
value of `a - b`. It must store the tree for later.

### Applying UPDATE values one at a time

Mutating the row between expression evaluations causes later assignments to
observe earlier updates. Evaluate all expressions first.

### Continuing to use column indexes for SELECT

`lookupColumns()` and `subsetRow()` can select stored columns, but they cannot
calculate a new value such as `a * 4 - b`. Use `evalExpr()`.

### Changing WHERE too early

In 0505, WHERE is still responsible only for identifying the row by its key.
Do not mix the next chapter's WHERE-expression work into this chapter.

### Confusing the destination with the expression

In:

```sql
SET a = b + 1
```

`a` is the destination column. It is not part of the right-hand expression
tree. The expression tree represents only `b + 1`.

---

## 16. Final Mental Model

For SELECT:

```text
SELECT a + b
       └── parseExpr() builds a tree

fetch row containing a and b
       └── evalExpr() calculates the tree

calculated Cell
       └── placed in the returned Row
```

For UPDATE:

```text
SET a = a - b
    │   └── expression tree to evaluate later
    └────── destination column

fetch original row
       └── evaluate every assignment tree

calculated NamedCell values
       └── apply together and save the row
```

The shortest summary is:

> 0504 made expressions understandable. 0505 makes SELECT and UPDATE use
> them.

---

## Chapter Completion Checklist

- [ ] `StmtSelect.cols` stores expression nodes.
- [ ] `parseSelect()` parses each selected item with `parseExpr()`.
- [ ] `ExprAssign` stores a destination column and an expression tree.
- [ ] `StmtUpdate.value` stores `ExprAssign` values.
- [ ] `parseAssign()` parses `column = expression`.
- [ ] `parseUpdate()` uses `parseAssign()`.
- [ ] `execSelect()` evaluates every selected expression.
- [ ] `execUpdate()` evaluates every assignment against the original row.
- [ ] UPDATE results are applied only after all expressions are evaluated.
- [ ] SELECT headers remain valid strings.
- [ ] Parser tests show the expression-tree structure.
- [ ] Execution tests log input rows, evaluation, and final results.
- [ ] Existing INSERT, DELETE, and WHERE behavior still passes its tests.
- [ ] `gofmt` and `go test -v ./0505` pass.
