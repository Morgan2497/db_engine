# Chapter 0303: SELECT Statement AST (Point Queries)

## Overview: First Real SQL Shape
0301–0302 can tokenize names and parse literals. This chapter builds the first **statement AST**: a rigid `SELECT` that maps 1:1 onto a primary-key KV lookup.

```text
SQL string ──parseSelect──► StmtSelect ──(later Exec)──► EncodeKey + Get
```

**Intentional limit:** exact PK match only. No `OR`, ranges, joins, or aggregates. That keeps the AST a thin wrapper over storage.

---

## The Concept & Theory: Why an AST at All?

### Bytes In, Structure Out

After lexing/values, you still only have a linear cursor. An **Abstract Syntax Tree (AST)** reifies the *shape* of the statement:
* which table,
* which columns to return (projection),
* which equality predicates identify the row.

The executor should not re-scan SQL text. It should walk a small struct. That boundary — **parser produces AST; executor consumes AST** — is how real engines isolate syntax from runtime.

### Restricting the Grammar Is a Feature

Full SQL `WHERE` is a boolean expression tree (`AND`/`OR`/comparisons/functions). Supporting that demands binding, type checking, and a query planner. By allowing only:

```text
col = value [AND col = value]*
```

we guarantee the predicate *is* a primary-key specification (once validated against the schema in 0305). The AST maps onto `EncodeKey` without optimization puzzles.

This is **progressive disclosure**: teach the PK point-query path that optimizers love, before general filters.

### Named Bindings (`NamedCell`)

A bare `Cell` is untyped relative to columns. `NamedCell` pairs **column name + value**, which is exactly what WHERE/SET clauses express in SQL. Execution later resolves names → schema indices (`lookupColumns` / `makePKey`). Keeping names in the AST preserves SQL’s user-facing vocabulary until the catalog binds them.

### Projection vs Selection (Relational Vocab)

In relational algebra:
* **Selection (σ)** — filter rows (our WHERE).
* **Projection (π)** — choose columns (our SELECT list).

Even in this tiny engine, `StmtSelect` already separates those ideas: `keys` filter identity; `cols` shape the output. That vocabulary will matter when scans and multiple rows arrive.

---

## 1. AST Types

```go
type NamedCell struct {
    column string
    value  Cell
}

type StmtSelect struct {
    table string
    cols  []string    // projection list
    keys  []NamedCell // WHERE equalities (PK)
}
```

### Skeleton visualization

```sql
SELECT a, b FROM t WHERE c=1 AND d='e';
```

```text
StmtSelect
┌────────┬─────────────────┐
│ table  │ "t"             │
│ cols   │ ["a", "b"]      │
│ keys   │                 │
│        │  c → Cell(1)    │
│        │  d → Cell("e")  │
└────────┴─────────────────┘
```

---

## 2. Grammar

```text
select_stmt    ::= SELECT col_list FROM table WHERE predicate_list ';'
col_list       ::= name (',' name)*
predicate      ::= name '=' value
predicate_list ::= predicate ('AND' predicate)*
```

```text
SELECT ──► columns ──► FROM ──► table ──► WHERE ──► eqs ──► ;
```

---

## 3. Parse Tree Mock

```text
                    parseSelect
                         │
         ┌───────────────┼────────────────┐
         ▼               ▼                ▼
     col_list         table           parseWhere
     a , b              t            c=1 AND d='e'
                                         │
                              ┌──────────┴──────────┐
                              ▼                     ▼
                         parseEqual            parseEqual
                         c = 1                 d = 'e'
```

`parseWhere` is a **flat AND loop**, not a recursive boolean expression tree — perfect for “all these equalities form the PK.”

---

## 4. `parseEqual` Mini-Example

```text
Input:  foo = 123

NamedCell{
  column: "foo",
  value:  Cell{Type: TypeI64, I64: 123},
}
```

```text
name ── tryPunctuation('=') ── parseValue ──► NamedCell
```

---

## 5. How This Maps to Storage (Mental Model)

```text
StmtSelect.keys  →  make PK row cells  →  EncodeKey(schema)
StmtSelect.cols  →  projection after DecodeVal
StmtSelect.table →  schema lookup / table prefix
```

```text
WHERE c=1 AND d='e'
        │
        ▼
KV key ≈  t\0 + Enc(c=1) + Enc(d='e')   (once schema known in 0305)
```

---

## 6. What Is Explicitly Out of Scope

| Feature | Status |
| :--- | :--- |
| `OR`, parentheses | ✗ |
| `WHERE age > 20` | ✗ (needs ordered indexes later) |
| `SELECT *` without PK | ✗ (no table scan yet) |
| INSERT/UPDATE/DELETE | → 0304 |

---

## Crucial Takeaways

* First AST bridges lexer/values to executable structure.
* SELECT is constrained to **point queries by PK equalities**.
* `NamedCell` binds a column name to a disk-ready `Cell`.
* Next: router for **all statement kinds** (0304).
