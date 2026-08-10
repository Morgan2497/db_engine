# Chapter 0504: Expression Parser

## Overview

Chapter 0504 turns the small arithmetic parser from the previous chapters into
a general SQL expression parser.

The progression is:

```text
0501  Parse atoms, +, and - into expression trees
0502  Evaluate expression trees recursively
0503  Add *, /, precedence, and parentheses
0504  Add comparisons, Boolean operators, and prefix operators
```

The central goal is:

> Convert a flat SQL expression into a tree whose structure correctly records
> operator precedence, associativity, and parentheses.

For example:

```text
f OR e AND NOT d = a + b * -c
```

becomes:

```text
                OR
               /  \
              f   AND
                 /   \
                e    NOT
                      |
                      =
                     / \
                    d   +
                       / \
                      a   *
                         / \
                        b  NEG
                            |
                            c
```

The parser builds this tree only. Later chapters connect expression parsing to
`SELECT`, `UPDATE`, and `WHERE` execution.

---

## 1. Complete Operator Precedence

Chapter 0504 supports these operators, listed from lowest precedence to
highest:

| Precedence | Operators | Parser function |
|---:|---|---|
| 1, lowest | `OR` | `parseOr()` |
| 2 | `AND` | `parseAnd()` |
| 3 | prefix `NOT` | `parseNot()` |
| 4 | `=`, `!=`, `<>`, `<`, `>`, `<=`, `>=` | `parseCmp()` |
| 5 | `+`, `-` | `parseAdd()` |
| 6 | `*`, `/` | `parseMul()` |
| 7 | prefix `-` | `parseNeg()` |
| 8, highest | names, literals, parentheses | `parseAtom()` |

Lower-precedence operators appear closer to the root of the tree. Tighter
operators become deeper subtrees.

For:

```text
a OR b + c * -d
```

the grouping is:

```text
a OR (b + (c * (-d)))
```

and the tree is:

```text
       OR
      /  \
     a    +
         / \
        b   *
           / \
          c  NEG
              |
              d
```

---

## 2. Parser Call Chain

The top-level entry point is:

```go
func (p *Parser) parseExpr() (interface{}, error) {
    return p.parseOr()
}
```

The complete call chain is:

```text
parseExpr
└── parseOr
    └── parseAnd
        └── parseNot
            └── parseCmp
                └── parseAdd
                    └── parseMul
                        └── parseNeg
                            └── parseAtom
```

Each function owns one precedence level. To obtain an operand, it calls the
next tighter parser beneath it.

```text
parseOr  gets operands from parseAnd
parseAnd gets operands from parseNot
parseCmp gets operands from parseAdd
parseAdd gets operands from parseMul
parseMul gets operands from parseNeg
parseNeg gets its child from parseNeg or parseAtom
```

This gives every function a small, precise responsibility.

---

## 3. Grammar as a Design Plan

The parser can be described with these grammar rules:

```text
expression = or

or         = and (OR and)*
and        = not (AND not)*
not        = NOT not | comparison
comparison = addition (comparison-op addition)*
addition   = multiplication ((+ | -) multiplication)*
multiply   = negation ((* | /) negation)*
negation   = - negation | atom
atom       = name | literal | ( expression )
```

The `*` after a group means “zero or more repetitions.” For example:

```text
addition = multiplication ((+ | -) multiplication)*
```

means:

1. Parse one multiplication expression.
2. Look for `+` or `-`.
3. If one is found, parse another multiplication expression.
4. Repeat until neither operator appears.

---

## 4. Binary and Unary Nodes

A binary operator has two children:

```text
a + b

    +
   / \
  a   b
```

It uses:

```go
type ExprBinOp struct {
    op    ExprOp
    left  interface{}
    right interface{}
}
```

A unary operator has one child:

```text
-a       NOT a

NEG      NOT
 |        |
 a        a
```

It uses:

```go
type ExprUnOp struct {
    op  ExprOp
    kid interface{}
}
```

The word `kid` means the unary node's single child expression.

---

## 5. Why `parseBinop()` Exists

The binary precedence levels all use the same algorithm:

```text
parse first operand

while an owned operator follows:
    parse right operand
    build a binary node
    make that node the expression constructed so far

return the expression
```

They differ only in:

- which textual tokens they recognize;
- which `ExprOp` values those tokens represent;
- which tighter function parses their operands.

`parseBinop()` receives those differences as parameters:

```go
func (p *Parser) parseBinop(
    tokens []string,
    ops []ExprOp,
    inner func() (interface{}, error),
) (interface{}, error)
```

For addition:

```go
p.parseBinop(
    []string{"+", "-"},
    []ExprOp{OP_ADD, OP_SUB},
    p.parseMul,
)
```

This means:

```text
Recognize:        + or -
Store as:         OP_ADD or OP_SUB
Parse operands:   with parseMul()
```

For comparisons:

```text
"="  → OP_EQ
"!=" → OP_NE
"<>" → OP_NE
"<=" → OP_LE
">=" → OP_GE
"<"  → OP_LT
">"  → OP_GT
```

Both `!=` and `<>` map to the same internal not-equal operator.

---

## 6. Understanding the Two Loops

The outer loop asks:

> Is there another operator at this precedence level?

The inner loop asks:

> Which one of my allowed operators is next?

For `a + b - c`, the addition parser receives:

```text
tokens = [+, -]
```

Execution is:

```text
initial left = a

outer iteration 1:
    inner loop tries + → match
    right = b
    left = (a + b)

outer iteration 2:
    inner loop tries + → no
    inner loop tries - → match
    right = c
    left = ((a + b) - c)

outer iteration 3:
    neither + nor - matches
    stop
```

The `matched` Boolean tells the outer loop whether the inner loop found an
operator. If nothing matched, the expression at this level is complete.

---

## 7. The `left` Invariant

The most important rule inside `parseBinop()` is:

> `left` always contains the complete expression constructed so far.

For:

```text
a - b + c
```

`left` changes as follows:

```text
a
(a - b)
((a - b) + c)
```

Every new node stores the previous tree as its left child:

```go
left = &ExprBinOp{
    op:    operator,
    left:  left,
    right: right,
}
```

This naturally produces left associativity:

```text
a - b - c → (a - b) - c
```

---

## 8. The `inner` Function and Precedence

`inner` is the function that parses tighter-binding operands.

At the addition level:

```text
inner = parseMul
```

For:

```text
a - b * c
```

the addition parser receives:

```text
left  = parseMul() → a
right = parseMul() → b * c
```

It then builds:

```text
a - (b * c)
```

The addition parser never needs special knowledge about multiplication.
`parseMul()` guarantees that its complete subtree is returned first.

---

## 9. Recursive Prefix Operators

Binary operators use loops because they are left-associative. Prefix unary
operators use recursion because one prefix can contain another at the same
level.

For:

```text
--a
```

`parseNeg()` behaves like:

```text
see -
└── call parseNeg
    see -
    └── call parseNeg
        parse atom a
    return NEG(a)
return NEG(NEG(a))
```

Tree:

```text
NEG
 |
NEG
 |
 a
```

`parseNot()` uses the same pattern:

```text
NOT NOT a

NOT
 |
NOT
 |
 a
```

---

## 10. Parentheses

Parentheses are handled in `parseAtom()`, the tightest parser level.

When `parseAtom()` sees `(`, it:

1. consumes `(`;
2. calls the top-level `parseExpr()`;
3. parses the complete expression inside;
4. requires `)`;
5. returns the inner tree as one atom.

For:

```text
(a + b) * c
```

the parenthesized subtree is produced first:

```text
    +
   / \
  a   b
```

Then multiplication uses it as its left operand:

```text
        *
       / \
      +   c
     / \
    a   b
```

Parentheses do not create a special node. They control grouping during parsing.

---

## 11. Unary Evaluation

Chapter 0504 extends the tree-walk evaluator to recognize `ExprUnOp`.

The evaluator first recursively evaluates the child:

```text
evaluate kid
     ↓
apply unary operator
     ↓
return new Cell
```

### Numeric negation

```text
-20 → -20
--20 → 20
```

`OP_NEG` is supported only for `TypeI64`. Applying it to a string returns a
`bad unary op` error.

### Logical NOT

This chapter uses integer truth values:

```text
0       = false
nonzero = true
```

`OP_NOT` normalizes its result to either `0` or `1`:

```text
NOT 0  → 1
NOT 1  → 0
NOT 42 → 0
```

Repeated prefix nodes evaluate from the deepest child outward:

```text
--a

evaluate a
negate it
negate the result again
```

---

## 12. Worked Example

Consider:

```text
(a + b) - c AND e
```

The parser reasons by precedence level:

```text
Parentheses:
    (a + b)

Addition/subtraction:
    ((a + b) - c)

AND:
    ((a + b) - c) AND e
```

Node creation order:

```text
N1 = ADD(a, b)
N2 = SUB(N1, c)
N3 = AND(N2, e)
```

Final tree:

```text
          AND
         /   \
        -     e
       / \
      +   c
     / \
    a   b
```

---

## 13. Testing Strategy

The parser tests should validate properties rather than every individual call:

| Test | Property proved |
|---|---|
| `a - b + c` | Same-level operators associate left |
| `a + b * c` | Multiplication binds tighter than addition |
| `(a + b) * c` | Parentheses override precedence |
| `a OR b AND c` | AND binds tighter than OR |
| `NOT NOT a` | Repeated keyword prefixes recurse |
| `--a` | Repeated negation recurses |
| `a != b`, `a <> b` | Both not-equal spellings normalize to `OP_NE` |
| Complete precedence expression | Every parser level composes correctly |

Evaluator tests cover:

```text
-a
--a
NOT 0
NOT 1
NOT nonzero
-"Sales" → error
```

Run all Chapter 0504 tests:

```bash
go test -v ./0504
```

From inside the `0504` directory:

```bash
go test -v
```

---

## Review Summary

The three core parser rules are:

```text
1. Each function owns exactly one precedence level.
2. Each binary function asks the next tighter function for operands.
3. Each function stops when the next token belongs to its caller.
```

`parseBinop()` provides the reusable binary pattern:

```text
left = parse tighter operand

while one of my operators matches:
    right = parse tighter operand
    left = new binary node(operator, left, right)

return left
```

Unary parsers use recursion:

```text
NOT NOT a → NOT(NOT(a))
--a       → NEG(NEG(a))
```

The complete mental model is:

```text
flat SQL expression
        ↓ precedence parser
binary/unary expression tree
        ↓ recursive evaluator
typed Cell result
```
