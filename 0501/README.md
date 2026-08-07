# Chapter 0501: Infix Expressions

## Overview

Chapter 0405 added range scans at the storage and relational layers. The next
problem is expressing more complicated SQL operations. Earlier chapters could
parse a rigid `WHERE` clause such as:

```sql
SELECT a, b FROM t WHERE a = 123 AND b = 456;
```

That representation works when every condition has the same fixed shape:

```text
column = literal
```

It does not generalize to expressions such as:

```sql
SELECT a + b FROM t WHERE a > 123;
```

or:

```sql
SELECT a, b FROM t WHERE (a, b) > (123, 0);
```

A database needs to preserve the structure of these expressions. It must know:

- which values are operands;
- which operator joins those operands;
- which operation happens first;
- how parentheses affect that order;
- whether a token is a column reference or a literal value.

Chapter 0501 begins solving this problem by representing an expression as a
tree. It then introduces the smallest expression parser, `parseAtom()`, and
uses atoms as the leaves from which larger expression trees can be built.

This chapter intentionally handles only a small subset of expression parsing:

```text
atoms
addition
subtraction
```

Expression evaluation, multiplication and division, multiple precedence
levels, parentheses, and the complete collection of SQL operators are added in
later chapters.

---

## 1. What Is an Expression?

An expression is a piece of syntax that produces a value.

Simple expressions include:

```text
a
123
"Sales"
```

Compound expressions combine smaller expressions with operators:

```text
a + b
a - 3
a + b * c
```

In:

```text
a + b
```

the parts are:

```text
left operand    operator    right operand
     a             +              b
```

The left and right operands can themselves be expressions. For example, in:

```text
a + (b * c)
```

the right operand of `+` is the complete expression `b * c`.

This recursive property is why a tree is a natural representation.

---

## 2. Representing Expressions as Trees

The expression:

```text
a + b
```

can be represented as:

```text
    +
   / \
  a   b
```

The root contains the operator. Its children contain the operands.

A more complicated expression:

```text
a + (b * c)
```

becomes:

```text
      +
     / \
    a   *
       / \
      b   c
```

The tree makes the evaluation order explicit:

1. Find the deepest operation, `b * c`.
2. Evaluate that operation.
3. Use its result as the right operand of `+`.
4. Evaluate `a + result`.

The original SQL text is a flat sequence of characters. The expression tree
turns that sequence into an unambiguous structure.

### Leaves and internal nodes

Expression-tree nodes have two broad roles:

```text
Leaf node       → a value that cannot be divided further in this chapter
Internal node   → an operator that connects child expressions
```

Examples of leaves:

```text
column name:    a
integer:        123
string:         "Sales"
```

Example of an internal node:

```text
    +
   / \
  a  123
```

In Chapter 0501, `parseAtom()` produces leaf nodes. `parseAdd()` later joins
those leaves into binary-expression nodes.

---

## 3. Infix, Prefix, and Postfix Notation

Infix, prefix, and postfix are three ways to write the same expression. The
difference is the position of the operator relative to its operands.

### Infix notation

In infix notation, the operator appears between its operands:

```text
a + b
```

```text
left operand    operator    right operand
     a             +              b
```

SQL and ordinary mathematics primarily use infix notation:

```sql
price + tax
age > 18
active AND verified
```

Infix notation is familiar to people, but its structure is not always explicit.
Consider:

```text
a + b * c
```

The text alone does not show the tree unless the reader knows the precedence
rules. It could be grouped as either:

```text
(a + b) * c
```

or:

```text
a + (b * c)
```

Normal arithmetic precedence selects the second form.

### Prefix notation

In prefix notation, the operator appears before its operands:

```text
+ a b
```

This is the prefix form of:

```text
a + b
```

The nested infix expression:

```text
a + (b * c)
```

becomes:

```text
+ a (* b c)
```

With complete parentheses:

```text
(+ a (* b c))
```

Read it from the outside inward:

```text
+
├── left operand:  a
└── right operand: (* b c)
```

The right operand is another prefix expression:

```text
*
├── left operand:  b
└── right operand: c
```

That structure maps directly to this tree:

```text
      +
     / \
    a   *
       / \
      b   c
```

### Postfix notation

In postfix notation, the operator appears after its operands:

```text
a b +
```

The nested expression:

```text
a + (b * c)
```

becomes:

```text
a b c * +
```

Postfix notation can be evaluated with an explicit stack:

```text
Read a → push a
Read b → push b
Read c → push c
Read * → pop b and c, compute b*c, push the result
Read + → pop a and the previous result, compute a+result
```

For `a = 2`, `b = 3`, and `c = 4`:

```text
2 3 4 * +
    └─┬─┘
     12

2 12 +
└──┬──┘
   14
```

### Comparing the three forms

The following expressions all represent the same tree:

| Notation | Expression | Operator position |
|---|---|---|
| Infix | `a + (b * c)` | Between operands |
| Prefix | `+ a (* b c)` | Before operands |
| Postfix | `a b c * +` | After operands |

---

## 4. Why Prefix Notation Mirrors the Tree Well

Prefix notation is especially useful when thinking about recursive expression
trees because the first item names the operation represented by the current
node:

```text
(+ a (* b c))
```

The expression begins with `+`, so the root is immediately known to be `+`.
The next two expressions are its children:

```text
first child:   a
second child:  (* b c)
```

The second child begins with `*`, so it is another internal node with children
`b` and `c`.

This is the same recursive shape as the tree:

```text
Expr
├── operator
├── left Expr
└── right Expr
```

Prefix notation has several advantages for understanding tree structure:

- the operator for a node appears first;
- its operands follow it directly;
- nested expressions identify their own operators first;
- precedence is encoded by nesting instead of assumed from convention;
- a recursive parser or evaluator can follow the representation naturally.

For example, compare these two infix expressions:

```text
a + b * c
(a + b) * c
```

Their prefix forms reveal the different roots immediately:

```text
+ a (* b c)    // root is +
* (+ a b) c    // root is *
```

It is important not to overstate this advantage. Prefix notation is not always
universally better than postfix notation. Postfix is excellent for stack-based
evaluation. Infix remains preferable as user-facing SQL because it is familiar
to people. For this database, the useful distinction is:

```text
SQL input          → infix, convenient for humans
expression tree    → structurally similar to prefix, convenient for recursion
postfix            → convenient for explicit stack evaluation
```

The database does not need to change the SQL language into a prefix language.
It accepts infix SQL and converts it into a tree whose shape can be understood
like a prefix expression.

---

## 5. Operator Precedence

Operator precedence, also called operator priority, decides which kind of
operator binds more tightly.

Multiplication has higher precedence than addition. Therefore:

```text
a + b * c
```

means:

```text
a + (b * c)
```

and not:

```text
(a + b) * c
```

If `a = 2`, `b = 3`, and `c = 4`, the normal interpretation produces:

```text
2 + 3 * 4
2 + 12
14
```

Changing the grouping changes the answer:

```text
(2 + 3) * 4
5 * 4
20
```

The parser must therefore build the correct tree.

Normal precedence produces:

```text
      +
     / \
    a   *
       / \
      b   c
```

Explicit parentheses can produce the other tree:

```text
        *
       / \
      +   c
     / \
    a   b
```

The deepest operations are evaluated first, so tree shape determines execution
order.

### Precedence versus associativity

Precedence decides how different kinds of operators group:

```text
a + b * c
```

Because `*` has higher precedence than `+`:

```text
a + (b * c)
```

Associativity decides how operators at the same precedence level group.
Addition and subtraction have the same precedence and are left-associative:

```text
a + b - c
```

means:

```text
(a + b) - c
```

Its tree is:

```text
        -
       / \
      +   c
     / \
    a   b
```

Left associativity matters especially for subtraction:

```text
a - b - c
```

means:

```text
(a - b) - c
```

not:

```text
a - (b - c)
```

For `a = 10`, `b = 3`, and `c = 2`:

```text
(10 - 3) - 2 = 5
10 - (3 - 2) = 9
```

Chapter 0501 handles only `+` and `-`. Because they share one precedence level,
the parser can build the tree from left to right. Multiple precedence levels
are introduced in a later chapter.

---

## 6. The Expression Data Model

An operator is represented by `ExprOp`:

```go
type ExprOp uint8
```

Chapter 0501 adds operator identifiers for addition and subtraction:

```text
OP_ADD → +
OP_SUB → -
```

These constants do not store the characters `+` and `-`. They are internal
numeric labels. The parser will later perform this mapping:

```text
input punctuation "+" → OP_ADD
input punctuation "-" → OP_SUB
```

A binary expression contains one operator and two child expressions:

```go
type ExprBinOp struct {
    op    ExprOp
    left  interface{}
    right interface{}
}
```

For:

```text
a + b
```

the conceptual representation is:

```text
ExprBinOp
├── op:    OP_ADD
├── left:  "a"
└── right: "b"
```

For:

```text
a + b - 3
```

left associativity produces:

```text
ExprBinOp OP_SUB
├── left: ExprBinOp OP_ADD
│   ├── left:  "a"
│   └── right: "b"
└── right: Cell(3)
```

The fields use `interface{}` because a child expression can have different Go
types:

```text
string       → column reference
*Cell        → literal value
*ExprBinOp   → nested binary expression
```

Conceptually:

```text
Expression = string OR *Cell OR *ExprBinOp
```

Later evaluation code can use a Go type switch to determine which kind of
expression it received.

---

## 7. Understanding `parseAtom()`

### Its responsibility

`parseAtom()` parses one leaf expression.

In this chapter, an atom can be:

```text
column name
integer literal
string literal
```

Examples:

| Input | Meaning | Returned concrete type |
|---|---|---|
| `price` | Column reference | `string` |
| `_count` | Column reference | `string` |
| `123` | Integer literal | `*Cell` |
| `-123` | Negative integer literal | `*Cell` |
| `"Sales"` | String literal | `*Cell` |

The following is not one atom:

```text
price + 123
```

It contains:

```text
left atom:     price
operator:      +
right atom:    123
```

`parseAtom()` parses the two leaves. A higher-level parser is responsible for
consuming the operator and joining those leaves into an `ExprBinOp`.

### Its return type

The function returns:

```go
(interface{}, error)
```

The dynamic type stored inside `interface{}` communicates what kind of leaf was
parsed:

```text
string → look up this column during evaluation
*Cell  → this is already a constant value
```

Examples:

```text
input:  price
output: string("price")
```

```text
input:  123
output: *Cell{Type: TypeI64, I64: 123}
```

```text
input:  "Sales"
output: *Cell{Type: TypeStr, Str: []byte("Sales")}
```

The literal is returned as a `*Cell` so later code can distinguish it from a
column-name string using a type switch.

### Its decision process

The conceptual algorithm is:

```text
Try to parse a column name
        │
        ├── success → return the name
        │
        └── failure
              ↓
        Try to parse a literal value
              │
              ├── success → return a pointer to the Cell
              │
              └── failure → return the error
```

The existing parser already knows how to parse both possibilities:

- `tryName()` recognizes identifiers;
- `parseValue()` recognizes integer and string literals.

`parseAtom()` coordinates these helpers. It does not duplicate their parsing
logic.

### Why try a name first?

Names and literals normally begin with different characters:

```text
price    begins with a letter
_count   begins with an underscore
123      begins with a digit
"hello"  begins with a quote
'hello'  begins with a quote
```

If `tryName()` succeeds, the parser has found a column reference. If it fails,
`parseValue()` gets the opportunity to recognize a literal.

### Cursor behavior

`Parser` is a cursor over the input string:

```go
type Parser struct {
    buf string
    pos int
}
```

Successful parsing advances `pos` only past the atom. It must leave the rest of
the expression for the caller.

Consider:

```text
  price + 10
```

At first, the cursor is at the beginning:

```text
  price + 10
↑
pos
```

`tryName()` skips whitespace and consumes `price`:

```text
  price + 10
       ↑
      pos
```

`parseAtom()` returns `"price"`. It does not consume `+ 10`; those characters
belong to the higher-level expression parser.

For a literal:

```text
  123 + total
```

`tryName()` fails because a digit cannot begin an identifier. `parseValue()`
then consumes `123`:

```text
  123 + total
     ↑
    pos
```

The returned value is a pointer to an integer `Cell`, and `+ total` remains for
the caller.

### Error behavior

If the input is neither a name nor a literal, parsing fails.

For example:

```text
@
```

`tryName()` fails because `@` cannot begin an identifier. `parseValue()` also
fails because `@` is not a digit, sign, or quote. `parseAtom()` therefore
returns an error.

This behavior also lets a higher-level parser detect a missing operand:

```text
a +
```

After `+` is consumed, the parser asks for another atom. It reaches the end of
the input, so parsing the right operand fails.

### Negative literals versus subtraction

The existing value parser accepts signed integer literals:

```text
-123
+123
```

Therefore, `-123` can be one atom:

```text
Cell{Type: TypeI64, I64: -123}
```

By contrast:

```text
a - 123
```

contains two atoms and one binary operator:

```text
atom:      a
operator:  -
atom:      123
```

The higher-level parser consumes the `-` operator before asking `parseAtom()`
to parse `123`.

An expression such as:

```text
a + -123
```

can be divided into:

```text
atom:      a
operator:  +
atom:      -123
```

However, unary negation of a column:

```text
-a
```

is not a negative integer literal. It is a unary expression and is handled in
a later chapter.

### What `parseAtom()` does not do yet

`parseAtom()` does not:

- parse binary `+` or `-` operators;
- construct `ExprBinOp` nodes;
- evaluate a column or literal;
- perform arithmetic;
- enforce precedence;
- require the entire input to be consumed;
- parse parentheses in Chapter 0501.

Its complete responsibility is:

> Consume one leaf expression and return its representation.

For:

```text
a + b - 3
```

separate calls to `parseAtom()` will eventually produce:

```text
"a"
"b"
&Cell{Type: TypeI64, I64: 3}
```

The higher-level parser will later connect them into:

```text
        -
       / \
      +   3
     / \
    a   b
```

---

## 8. Testing `parseAtom()`

Tests should cover each valid leaf type and at least one invalid token:

| Input | Expected result |
|---|---|
| `foo` | `string("foo")` |
| `123` | integer `*Cell` containing `123` |
| `"Sales"` | string `*Cell` containing `Sales` |
| `-123` | integer `*Cell` containing `-123` |
| `@` | error |

A successful standalone-atom test should verify three things:

1. No error was returned.
2. The returned expression has the expected concrete type and value.
3. The parser reached the end of the input after optional whitespace.

Testing cursor boundaries separately is also valuable. Given:

```text
foo + 123
```

one call to `parseAtom()` should return `"foo"` while leaving `+ 123`
unconsumed.

---

## 9. How `parseAtom()` Fits Into the Parser

The parser is built in layers. Each layer consumes one kind of grammar and
delegates smaller pieces to the layer beneath it:

```text
parseAdd
   ↓ asks for operands
parseAtom
   ├── tryName
   └── parseValue
          ├── parseInt
          └── parseString
```

For:

```text
a + b - 3
```

the eventual flow is conceptually:

```text
parseAdd
├── parseAtom → a
├── consume +
├── parseAtom → b
├── build (a + b)
├── consume -
├── parseAtom → 3
└── build ((a + b) - 3)
```

The tree grows from left to right:

```text
a

(a + b)

((a + b) - 3)
```

This left-growing construction produces the correct left associativity for
addition and subtraction.

---

## 10. Chapter Boundaries

Chapter 0501 establishes the expression-tree representation and basic infix
parsing. It does not complete the entire SQL expression system.

The progression is:

| Chapter | Main responsibility |
|---|---|
| 0501 | Expression trees, atoms, and basic `+`/`-` parsing |
| 0502 | Recursive expression evaluation |
| 0503 | `*`, `/`, precedence levels, and parentheses |
| 0504 | Full SQL operators and reusable parser layers |

Keeping these boundaries clear makes the implementation easier to understand.
Chapter 0501 answers:

> How can flat infix text become a structured expression tree?

It does not yet answer:

> How is that tree evaluated, type-checked, or integrated everywhere in SQL?

---

## Review Summary

An expression tree makes operation order explicit:

```text
a + b * c

      +
     / \
    a   *
       / \
      b   c
```

The three common textual forms are:

```text
Infix:   a + (b * c)
Prefix:  + a (* b c)
Postfix: a b c * +
```

Their main strengths are:

```text
Infix   → familiar human-facing syntax
Prefix  → directly exposes recursive tree structure
Postfix → convenient explicit-stack evaluation
```

Precedence determines how different operators group:

```text
a + b * c → a + (b * c)
```

Associativity determines how equal-priority operators group:

```text
a - b - c → (a - b) - c
```

Finally, `parseAtom()` parses one expression leaf:

```text
name           → string
literal value  → *Cell
invalid token  → error
```

The most important mental model for Chapter 0501 is:

```text
flat infix input
       ↓ parse
expression tree
       ↓ later evaluate recursively
result
```
