# Chapter 0503: Operator Priority

## Overview

Chapter 0501 introduced infix expressions and represented them as binary
expression trees. Chapter 0502 walked those trees to compute values. At that
point, the parser understood only one operator level:

```text
addition and subtraction: + -
```

That is not enough for ordinary arithmetic. Consider:

```text
a + b * c
```

The parser must not treat this as:

```text
(a + b) * c
```

Multiplication has higher priority than addition, so the intended meaning is:

```text
a + (b * c)
```

Chapter 0503 solves this structural problem by giving each priority level its
own recursive-descent parser function:

```text
parseExpr()
    ↓
parseAdd()      handles + and -
    ↓
parseMul()      handles * and /
    ↓
parseAtom()     handles names, values, and parenthesized expressions
```

The chapter also adds parentheses. Parentheses allow an expression to request
a grouping that differs from normal operator priority:

```text
a + b * c       → a + (b * c)
(a + b) * c     → (a + b) * c
```

Finally, the evaluator must learn how to execute the two new expression node
types:

```text
OP_MUL → integer multiplication
OP_DIV → integer division
```

The central idea of this chapter is:

> Precedence is encoded by the parser's call hierarchy. Higher-priority
> operations are parsed deeper in the expression tree.

---

## 1. Operator Priority, Precedence, and Associativity

### Operator priority and precedence

The terms **operator priority** and **operator precedence** describe the same
idea: when an expression contains different operators, which operator binds
to its operands first?

For the operators in this chapter:

| Priority | Operators | Parser function |
|---|---|---|
| Lower | `+`, `-` | `parseAdd()` |
| Higher | `*`, `/` | `parseMul()` |
| Highest | atoms and `(...)` | `parseAtom()` |

Given:

```text
a + b * c
```

`*` binds more tightly than `+`, producing:

```text
a + (b * c)
```

Tree:

```text
      +
     / \
    a   *
       / \
      b   c
```

The deepest binary node is evaluated first, so this structure records the
correct execution order.

### Associativity

Precedence decides grouping between **different priority levels**.
Associativity decides grouping between operators at the **same priority
level**.

For example, multiplication and division have equal precedence:

```text
a / b * c
```

These operators are left-associative, so the grouping is:

```text
(a / b) * c
```

not:

```text
a / (b * c)
```

Likewise:

```text
a - b + c → (a - b) + c
```

The parser loops from left to right and repeatedly stores the new tree in
`currentExpr`. That replacement creates left associativity.

### Precedence and associativity answer different questions

```text
a + b * c / d - e
```

Precedence first separates the expression into levels:

```text
a + (b * c / d) - e
```

Associativity then groups the equal-priority operations:

```text
(a + ((b * c) / d)) - e
```

---

## 2. Infix Input, Prefix Form, and Expression Trees

SQL arithmetic is written in infix notation:

```text
left-operand operator right-operand

a + b
b * c
d / e
```

Infix text is convenient for people, but it can be ambiguous unless the reader
also knows precedence and associativity rules.

Prefix notation places the operator before its operands:

```text
infix:   a + b
prefix:  (+ a b)
```

For the larger expression:

```text
a + b * c - d / e
```

the fully grouped prefix form is:

```text
(- (+ a (* b c)) (/ d e))
```

Prefix notation mirrors the expression tree directly:

```text
          -
         / \
        +   /
       / \ / \
      a  * d  e
        / \
       b   c
```

Read each internal tree node in this order:

```text
(operator left-subtree right-subtree)
```

For example:

```text
(* b c)
```

means:

```text
operator = *
left     = b
right    = c
```

The prefix representation used in the tests is not a second parser format. It
is a diagnostic rendering of the tree already created from infix SQL text.

---

## 3. The Binary Expression Data Model

All four arithmetic operators use the same node structure:

```go
type ExprBinOp struct {
    op    ExprOp
    left  interface{}
    right interface{}
}
```

The chapter adds two operator constants alongside addition and subtraction:

```go
OP_ADD // +
OP_SUB // -
OP_MUL // *
OP_DIV // /
```

The child fields use `interface{}` because a child may have one of several
concrete types:

```text
string       → column reference
*Cell        → literal value
*ExprBinOp   → nested binary expression
```

For:

```text
a + b * c
```

the root node conceptually contains:

```go
&ExprBinOp{
    op:   OP_ADD,
    left: "a",
    right: &ExprBinOp{
        op:    OP_MUL,
        left:  "b",
        right: "c",
    },
}
```

The node itself does not calculate anything. It only records structure.
Evaluation happens later in `evalExpr()`.

---

## 4. Recursive-Descent Parser Levels

A recursive-descent parser is organized as a collection of functions. Each
function recognizes one part of the grammar and calls other parser functions
for its components.

The 0503 expression grammar can be summarized as:

```text
expression     → addition

addition       → multiplication (("+" | "-") multiplication)*

multiplication → atom (("*" | "/") atom)*

atom           → "(" expression ")"
               | column-name
               | literal-value
```

Symbols in this grammar mean:

```text
|       choose one alternative
(...)*  repeat zero or more times
"+"     match this literal punctuation
```

### Why parsing begins at the lowest priority

`parseExpr()` begins with `parseAdd()`, even though addition has lower priority
than multiplication:

```go
func (p *Parser) parseExpr() (interface{}, error) {
    return p.parseAdd()
}
```

This initially seems backward. The root of the final tree, however, must hold
the operation performed last. Lower-priority operations are performed later,
so they belong nearer the root.

For:

```text
a + b * c
```

the lower-priority `+` becomes the root:

```text
      +       ← parsed by parseAdd()
     / \
    a   *     ← parsed by parseMul()
       / \
      b   c   ← parsed by parseAtom()
```

Thus the call hierarchy and tree depth agree:

```text
lower priority  → higher parser level → nearer tree root
higher priority → deeper parser call  → deeper tree node
```

---

## 5. Understanding `parseMul()`

`parseMul()` handles a chain containing only multiplication and division. Its
operands come from `parseAtom()`:

```text
atom * atom / atom * atom ...
```

Its algorithm is:

```text
1. Parse the first atom into currentExpr.
2. Check for * or /.
3. If neither operator is present, return currentExpr.
4. Parse the next atom.
5. Build a binary node from currentExpr, the operator, and the next atom.
6. Replace currentExpr with the new node.
7. Repeat from step 2.
```

### Detailed example: `a * b / c`

Initial state:

```text
input       = a * b / c
currentExpr = not assigned yet
```

First, `parseAtom()` reads `a`:

```text
currentExpr = "a"
remaining   = * b / c
```

The loop recognizes `*`, then `parseAtom()` reads `b`:

```text
operator = OP_MUL
nextAtom = "b"
```

It builds:

```text
(* a b)
```

and replaces `currentExpr`:

```text
currentExpr = (* a b)
remaining   = / c
```

The loop runs again, recognizes `/`, and reads `c`:

```text
operator = OP_DIV
nextAtom = "c"
```

The left child is the complete previous tree:

```text
(/ (* a b) c)
```

Tree:

```text
      /
     / \
    *   c
   / \
  a   b
```

This proves that the loop implements left associativity:

```text
(a * b) / c
```

When the next token is not `*` or `/`, `parseMul()` stops without consuming
that token. For example, while parsing:

```text
a * b + c
```

`parseMul()` returns the `a * b` subtree and leaves `+` for `parseAdd()`.

---

## 6. Connecting `parseAdd()` to `parseMul()`

Before this chapter, `parseAdd()` obtained both operands directly from
`parseAtom()`:

```text
parseAdd → parseAtom
```

That design cannot absorb a multiplication subtree. If the input is:

```text
a + b * c
```

an atom parser reads only `b`; it does not include `* c`.

Chapter 0503 changes both operand calls in `parseAdd()`:

```text
first operand: parseMul()
right operand: parseMul()
```

The new relationship is:

```text
parseAdd → parseMul → parseAtom
```

This means each operand of `+` or `-` may be an entire multiplication or
division subtree.

### Detailed mixed-priority example

Input:

```text
a + b * c - d / e
```

The first `parseMul()` reads `a`. It sees `+`, which is not its responsibility,
so it returns:

```text
a
```

`parseAdd()` consumes `+` and calls `parseMul()` again. This second call reads:

```text
b * c
```

and returns:

```text
(* b c)
```

`parseAdd()` combines its current left expression and this returned subtree:

```text
(+ a (* b c))
```

Next, `parseAdd()` consumes `-`. Its next `parseMul()` call reads:

```text
d / e
```

and returns:

```text
(/ d e)
```

The final tree becomes:

```text
(- (+ a (* b c)) (/ d e))
```

Tree:

```text
          -
         / \
        +   /
       / \ / \
      a  * d  e
        / \
       b   c
```

No function compares numeric priority values. The correct structure appears
because `parseAdd()` waits for each `parseMul()` call to finish.

---

## 7. Parentheses as Atoms

Normal precedence gives:

```text
a + b * c → a + (b * c)
```

Parentheses request a different grouping:

```text
(a + b) * c
```

The complete parenthesized expression behaves as one atom when viewed by the
outer multiplication parser.

This is why parentheses are handled in `parseAtom()`, the deepest parser
level.

### Parenthesis algorithm

Before trying a column name or literal value, `parseAtom()` performs:

```text
1. Try to consume "(".
2. If it is present, call parseExpr() recursively.
3. Propagate an error from the inner expression.
4. Require a matching ")".
5. Return the complete inner expression tree.
```

The important recursive jump is:

```text
parseAtom → parseExpr
```

This closes a cycle in the call graph:

```text
parseExpr
    ↓
parseAdd
    ↓
parseMul
    ↓
parseAtom
    └── when "(" is found, call parseExpr again
```

The recursion is safe because each parenthesis call consumes `(` before
recursing. The parser therefore advances through the input rather than calling
itself forever at the same cursor position.

### Detailed example: `(a + b) * c`

The outer call sequence begins:

```text
parseExpr → parseAdd → parseMul → parseAtom
```

`parseAtom()` consumes `(` and recursively calls `parseExpr()` at `a`.

The inner parser constructs:

```text
(+ a b)
```

It stops when it reaches `)` because `)` is not an arithmetic operator. The
inner parser does not consume the closing delimiter.

Control returns to the paused outer `parseAtom()`, which requires and consumes
`)`. It then returns the inner tree as a single atom:

```text
returned atom = (+ a b)
```

The outer `parseMul()` continues, consumes `*`, and reads `c` as its right
atom. It builds:

```text
(* (+ a b) c)
```

Tree:

```text
      *
     / \
    +   c
   / \
  a   b
```

The parentheses are not stored as tree nodes. Their effect is already encoded
in the shape of the returned tree.

### Nested parentheses

The recursive design naturally supports nesting:

```text
((a + b) * (c - d)) / e
```

Every opening parenthesis creates another `parseExpr()` call. Each call
returns only after its matching closing parenthesis is reached and consumed by
the `parseAtom()` that opened it.

---

## 8. Parser Cursor Ownership

The parser stores its position in:

```go
type Parser struct {
    buf string
    pos int
}
```

All expression parser functions share the same `*Parser`, so recursive calls
also share the same input buffer and cursor.

### Successful punctuation

`tryPunctuation()` skips leading whitespace, checks the requested symbol, and
advances past it on success:

```text
input:  "  * c"
start:     ↑ pos
after:       ↑ pos after matching *
```

### Failed punctuation

On a failed match, the punctuation itself is not consumed. Leading whitespace
may already have been skipped.

This allows one parser level to stop and leave an operator for its caller:

```text
parseMul sees +
    ↓
* does not match
/ does not match
    ↓
parseMul returns without consuming +
    ↓
parseAdd consumes +
```

### Closing-parenthesis ownership

The inner `parseExpr()` stops before `)` because no arithmetic loop recognizes
`)`. The outer `parseAtom()` that consumed `(` owns the responsibility for
consuming the matching `)`.

This ownership rule prevents inner parsers from accidentally consuming syntax
that belongs to their caller.

---

## 9. Parsing and Evaluation Remain Separate

Parsing answers:

```text
What does the expression mean structurally?
```

Evaluation answers:

```text
What value does this tree produce for a particular row?
```

For example:

```text
input SQL: a + b * c
```

Parsing produces:

```text
(+ a (* b c))
```

Given row values:

```text
a = 10
b = 4
c = 3
```

evaluation produces:

```text
b * c = 12
a + 12 = 22
```

`parseExpr()` does not access a schema or row. `evalExpr()` does not inspect
punctuation or decide precedence. The tree is the boundary between the two
phases:

```text
SQL characters
      ↓ parser
expression tree
      ↓ evaluator + schema + row
result Cell
```

---

## 10. Extending the Evaluator

Once the parser can create `OP_MUL` and `OP_DIV` nodes, `evalExpr()` must be
able to execute them.

The recursive part of the evaluator does not change:

```text
evaluate left subtree
evaluate right subtree
verify compatible types
apply the current operator
return a result Cell
```

The new integer cases are conceptually:

```text
OP_MUL + TypeI64 → left.I64 * right.I64
OP_DIV + TypeI64 → left.I64 / right.I64
```

### Multiplication

Given:

```text
left  = Cell(TypeI64, 6)
right = Cell(TypeI64, 4)
op    = OP_MUL
```

the result is:

```text
Cell(TypeI64, 24)
```

### Integer division

Both operands are `int64`, so division is integer division:

```text
7 / 2 = 3
```

The fractional portion is discarded. The parser does not control this rule;
it follows from the operand type used by the evaluator.

### Division by zero

Go integer division by zero panics. A database expression evaluator should
return an ordinary error rather than crashing the process:

```text
right.I64 == 0
      ↓
return division-by-zero error
```

The zero check must occur before executing `/`.

### Strong typing still applies

The evaluator must not guess conversions:

```text
integer * integer → valid
integer / integer → valid when the right value is nonzero
string  * string  → unsupported
integer * string  → type mismatch
```

Adding new operator constants does not automatically make them valid for
every `Cell` type. The operator and operand type must form a supported pair.

---

## 11. Error Behavior

Correct parsers report malformed input instead of returning a partial tree as
if it were complete.

### Missing right operand

Input:

```text
a *
```

Flow:

```text
parseMul reads a
parseMul consumes *
parseMul asks parseAtom for the right operand
parseAtom reaches the end and fails
parseMul propagates that error
```

The same pattern applies to:

```text
a /
a +
a -
```

### Missing closing parenthesis

Input:

```text
(a + b
```

The inner expression can still produce `(+ a b)`, but the outer
`parseAtom()` cannot find its required closing delimiter:

```text
tryPunctuation(")") → false
```

It returns:

```text
expect )
```

### Error propagation through recursion

If an inner parenthesized expression fails, the outer parser returns the same
error immediately:

```text
outer parseAtom
└── inner parseExpr
    └── error
        ↓
outer parseAtom returns error
```

No parent node should be created from a failed child expression.

---

## 12. Testing Operator Priority

The 0503 parser tests deliberately record three representations of each
expression:

```text
infix input
prefix representation
tree visualization
```

Example verbose output:

```text
[INPUT] "a + b * c - d / e"
[PREFIX] (- (+ a (* b c)) (/ d e))
[TREE]
-
├── left: +
│   ├── left: column "a"
│   └── right: *
│       ├── left: column "b"
│       └── right: column "c"
└── right: /
    ├── left: column "d"
    └── right: column "e"
```

Each representation serves a different purpose:

| Representation | What it verifies |
|---|---|
| Infix input | The original SQL-like expression |
| Prefix form | Exact grouping in one compact line |
| Tree | Parent/child relationships and depth |

### Important parser tests

| Test | Property demonstrated |
|---|---|
| `a * b / c` | `parseMul()` and left associativity |
| `a + b * c - d / e` | Mixed operator precedence |
| `(a + b) * c` | Parentheses override precedence |
| `(a + b` | Missing closing parenthesis returns an error |

The expected Go tree is also compared with `assert.Equal`. Logs help a person
understand a failure, while the structural assertion makes the test strict.

### Running the tests

From the repository root:

```bash
go test -v ./0503
```

From inside the `0503` directory:

```bash
go test -v
```

The `-v` flag is necessary to display `t.Log` output for passing tests.

To focus only on the new expression parser tests:

```bash
go test -v -run 'TestParseMul|TestParseExpr'
```

---

## 13. Common Implementation Mistakes

### Calling `parseAtom()` from `parseAdd()`

Incorrect relationship:

```text
parseAdd → parseAtom
```

This loses multiplication subtrees. Both operands in `parseAdd()` must come
from `parseMul()`.

### Putting multiplication logic in `parseExpr()`

`parseExpr()` is the entry point. It should delegate to the lowest-priority
parser rather than duplicate an operator loop:

```text
parseExpr → parseAdd
```

Multiplication logic belongs in `parseMul()`.

### Parsing the right side of multiplication with `parseMul()`

If `parseMul()` recursively called itself for every right operand, it would
change associativity. In this chapter, both sides of a multiplication step
come from `parseAtom()`, and the loop creates left associativity.

### Forgetting to replace `currentExpr`

After creating a node, the parser must perform:

```text
currentExpr = newNode
```

Without this replacement, a chain such as `a * b / c` cannot include the
previous operation as the next left child.

### Handling parentheses in `parseAdd()`

Parentheses are not an addition-only feature. Their contents may include any
expression operator, so they belong in `parseAtom()` and must recurse to the
top-level `parseExpr()`.

### Forgetting the closing parenthesis

Parsing the inner expression is only half the job. The opening `(` must be
paired with a required `)` or malformed input will be silently accepted.

### Mixing named and unnamed return values

If `parseAtom()` assigns to `expr` and `err` with `=`, both must already be
declared. A named-result form is:

```go
func (p *Parser) parseAtom() (expr interface{}, err error)
```

Writing:

```go
(expr interface{}, error)
```

mixes a named result with an unnamed result and does not compile.

### Expecting the tree to contain parentheses

Parentheses guide parsing but do not become nodes. These two inputs produce
equivalent trees:

```text
a + b
(a + b)
```

---

## 14. Chapter Scope

Chapter 0503 adds:

- multiplication and division operator nodes;
- a second binary-operator precedence level;
- left-associative `*` and `/` parsing;
- connection from `parseAdd()` to `parseMul()`;
- parenthesized expressions through recursion;
- multiplication and division evaluation;
- division-by-zero handling;
- tests that expose grouping through prefix form and trees.

It still does not provide the full SQL expression grammar. Chapter 0504 adds
more precedence levels and operator forms, including concepts such as:

```text
OR
AND
NOT
comparisons
unary negation
```

The design scales by inserting more parser functions into the same call chain:

```text
lower-priority parser
    ↓
next-higher-priority parser
    ↓
...
    ↓
parseAtom
```

---

## Review Summary

The chapter's precedence hierarchy is:

```text
parseExpr
    ↓
parseAdd       + -
    ↓
parseMul       * /
    ↓
parseAtom      names, literals, (...)
```

The grammar is:

```text
expression     → addition
addition       → multiplication ((+ | -) multiplication)*
multiplication → atom ((* | /) atom)*
atom           → (expression) | name | value
```

The parser achieves precedence because:

```text
parseAdd waits for parseMul
parseMul finishes a complete * or / subtree
parseAdd uses that subtree as one operand
```

The parser achieves left associativity because:

```text
build a node
replace currentExpr with that node
use it as the next left child
```

Parentheses work because:

```text
parseAtom consumes (
parseAtom recursively calls parseExpr
parseAtom requires )
parseAtom returns the inner tree as one atom
```

The compact prefix form of:

```text
a + b * c - d / e
```

is:

```text
(- (+ a (* b c)) (/ d e))
```

The most important mental model is:

> Lower-priority operators belong nearer the root. Higher-priority operators
> belong deeper in the tree. The parser's function-call hierarchy constructs
> that shape directly, and the evaluator later follows the shape without
> needing to reconsider precedence.
