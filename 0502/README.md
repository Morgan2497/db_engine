# Chapter 0502: Expression Evaluation

## Overview

Chapter 0501 converted flat infix text into an expression tree:

```text
a - b + 3

        +
       / \
      -   3
     / \
    a   b
```

Chapter 0502 gives that tree meaning. It recursively walks the tree, resolves
column names against a row, reads literal values, applies operators, and
returns the final `Cell`.

```text
SQL expression text
        ↓ 0501 parsing
expression tree
        ↓ 0502 evaluation
result Cell
```

This style of evaluator is called a **tree-walk interpreter**:

- **tree-walk**: it visits nodes in the expression tree;
- **interpreter**: it computes what each node means.

Chapter 0502 focuses on evaluation itself. Expressions are not connected to
`SELECT` and `UPDATE` until a later chapter.

---

## 1. The Evaluator's Inputs

The central function has this shape:

```go
func evalExpr(schema *Schema, row Row, expr interface{}) (*Cell, error)
```

Each argument answers a different question:

| Argument | Purpose |
|---|---|
| `schema` | Maps a column name to its position and declared type |
| `row` | Holds the current values for those columns |
| `expr` | The expression-tree node currently being evaluated |

For example:

```text
schema columns:  a,  b,  c
row values:     10,  4,  2
```

The schema lets the evaluator translate:

```text
"a" → column index 0 → row[0] → Cell(10)
"b" → column index 1 → row[1] → Cell(4)
"c" → column index 2 → row[2] → Cell(2)
```

The return value is always a `*Cell` because every successfully evaluated SQL
expression produces a typed database value.

---

## 2. The Three Expression Node Types

Chapter 0501 stores expressions using three concrete Go types:

```text
string       → column reference
*Cell        → literal value
*ExprBinOp   → binary operation with two child expressions
```

Because `expr` has type `interface{}`, `evalExpr()` uses a type switch to learn
which kind of node it received:

```text
switch expr's concrete type
├── string      → look up a column
├── *Cell       → return the literal
└── *ExprBinOp  → recursively evaluate both children
```

The first two cases are base cases: they return values without making another
recursive call. The binary-expression case is recursive.

---

## 3. Evaluating a Column Reference

A column expression is represented by a Go string:

```text
expr = "price"
```

The string is not the literal string value `"price"`. It means:

> Find the column named `price` in the schema and return its value from the
> current row.

Given:

```text
schema: [price, tax]
row:    [100,   8]
```

evaluation performs:

```text
find "price" in schema.Cols
        ↓
index = 0
        ↓
return row[0]
        ↓
Cell(100)
```

If the name does not exist, evaluation must return an error:

```text
expr = "missing"
        ↓
no matching schema column
        ↓
error: unknown column
```

This prevents an invalid expression from silently reading the wrong cell.

---

## 4. Evaluating a Literal

A literal was already converted into a `Cell` by the parser:

```text
123     → &Cell{Type: TypeI64, I64: 123}
"Sales" → &Cell{Type: TypeStr, Str: []byte("Sales")}
```

There is nothing left to resolve or compute. Evaluation simply returns that
cell:

```text
eval Cell(123)
      ↓
return Cell(123)
```

This is the simplest recursion base case.

---

## 5. Evaluating a Binary Expression

A binary node contains:

```text
operator
left child expression
right child expression
```

For:

```text
a - b
```

the node is:

```text
ExprBinOp
├── op:    OP_SUB
├── left:  "a"
└── right: "b"
```

The evaluator must process it in this order:

```text
1. Recursively evaluate the left child.
2. If that fails, return its error.
3. Recursively evaluate the right child.
4. If that fails, return its error.
5. Verify that the operand types are compatible.
6. Apply the node's operator.
7. Return a new result Cell.
```

Given `a = 10` and `b = 4`:

```text
eval "a" → Cell(10)
eval "b" → Cell(4)
OP_SUB    → 10 - 4
result    → Cell(6)
```

The result belongs to the current operation, so it is stored in a newly
created `Cell` rather than overwriting either operand.

---

## 6. Recursive Execution Flow

Consider:

```text
(a - b) + c
```

Tree:

```text
        +
       / \
      -   c
     / \
    a   b
```

With:

```text
a = 10
b = 4
c = 2
```

the call flow is:

```text
evalExpr(root +)
│
├── evalExpr(left -)
│   ├── evalExpr("a") → 10
│   ├── evalExpr("b") → 4
│   └── calculate 10 - 4 → 6
│
├── evalExpr("c") → 2
│
└── calculate 6 + 2 → 8
```

The outer `+` call pauses while the inner `-` subtree is evaluated. Go's call
stack remembers that paused work. When the inner call returns `6`, the outer
call continues and adds `2`.

This is why subtrees are evaluated before their parent node.

---

## 7. A Larger Example

Chapter 0501 builds this tree for:

```text
a - b + c - e
```

```text
            -
           / \
          +   e
         / \
        -   c
       / \
      a   b
```

Given:

```text
a = 20
b = 5
c = 3
e = 2
```

evaluation proceeds from the deepest subtree outward:

```text
20 - 5 = 15
15 + 3 = 18
18 - 2 = 16
```

Recursive trace:

```text
eval root -
├── eval +
│   ├── eval -
│   │   ├── eval a → 20
│   │   ├── eval b → 5
│   │   └── result → 15
│   ├── eval c → 3
│   └── result → 18
├── eval e → 2
└── result → 16
```

The returned value is:

```go
&Cell{Type: TypeI64, I64: 16}
```

---

## 8. Strong Typing

Before applying a binary operator, the evaluator must check operand types.

This is valid:

```text
123 + 456
integer + integer
```

This is invalid:

```text
123 + "abc"
integer + string
```

The evaluator should return an error rather than guessing how to convert one
operand:

```text
left.Type  = TypeI64
right.Type = TypeStr
        ↓
type mismatch error
```

For the current chapter, the clearly defined arithmetic operations are:

```text
TypeI64 + TypeI64 → TypeI64
TypeI64 - TypeI64 → TypeI64
```

Matching types alone do not guarantee that an operator is valid. For example,
two strings have matching types, but subtraction is not defined for strings.
An unsupported operator/type combination should return an error.

---

## 9. Error Propagation

Every recursive call can fail. A parent node cannot continue if either child
failed to produce a value.

```text
evaluate left
├── error → return immediately
└── success
      ↓
evaluate right
├── error → return immediately
└── success
      ↓
apply operator
```

Important evaluation errors include:

- unknown column name;
- mismatched operand types;
- unsupported operator;
- unsupported operand type;
- malformed expression node.

Returning the original child error preserves the location and reason for the
failure instead of attempting a calculation with an invalid operand.

---

## 10. Evaluation Does Not Parse

Parsing and evaluation are separate jobs:

```text
parseAdd()
    input:  characters such as "a - b + 3"
    output: ExprBinOp tree

evalExpr()
    input:  ExprBinOp tree + schema + row
    output: result Cell
```

`evalExpr()` does not consume SQL text, detect punctuation, or decide
associativity. Those decisions are already captured in the tree.

Likewise, `parseAdd()` does not know the values of `a` and `b`. It only records
their structural relationship.

---

## 11. Testing the Evaluator

The evaluator should be tested from the smallest base cases through nested
recursion:

| Test | What it proves |
|---|---|
| Literal `7` | A `*Cell` is a base case |
| Column `b` | Schema lookup selects the correct row cell |
| `a - b` | One binary node evaluates correctly |
| `a - b + c - e` | Nested nodes evaluate recursively |
| `123 + "abc"` | Strong typing rejects mismatched operands |
| Unknown column | Invalid column references return an error |
| Unsupported operation | Invalid operator/type pairs return an error |

Verbose test logs can display:

```text
[INPUT TREE]
[COLUMN LOOKUP]
[LEFT RESULT]
[RIGHT RESULT]
[OPERATION]
[FINAL RESULT]
```

This makes the recursive execution order visible during review.

---

## 12. Chapter Scope

Chapter 0502 adds the evaluator, but it does not yet add:

- multiplication or division;
- parentheses;
- full operator precedence;
- unary expressions;
- comparison and Boolean operators;
- expression parsing inside `SELECT` and `UPDATE`;
- expression-based `WHERE` filtering.

Those features build on the same tree-walk pattern in later chapters.

---

## Review Summary

The evaluator recognizes three node types:

```text
string      → resolve a column from schema + row
*Cell       → return a literal directly
*ExprBinOp  → recursively evaluate children, then apply the operator
```

The central recursive pattern is:

```text
evaluate left subtree
evaluate right subtree
check types
apply operator
return a new Cell
```

The complete chapter transition is:

```text
0501 builds:
    expression text → tree

0502 evaluates:
    tree + schema + row → Cell
```

The most important mental model is:

> Leaves produce values. Parent nodes combine those values. Recursion evaluates
> the deepest children first and works back toward the root.
