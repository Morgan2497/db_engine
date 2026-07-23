# Chapter 0304: Statements — Multi-Statement Router

## Overview: One Doorway for Every SQL Verb
0303 could parse only `SELECT`. A real engine needs DDL and DML too. This chapter adds a **recursive-descent dispatcher** `parseStmt()` that peeks at keywords and builds the right AST node.

```text
                    parseStmt()
                         │
     ┌──────────┬────────┼────────┬──────────┐
     ▼          ▼        ▼        ▼          ▼
  SELECT     CREATE   INSERT   UPDATE     DELETE
  StmtSelect StmtCreate… StmtInsert StmtUpdate StmtDelete
```

Return type is `interface{}` (polymorphic AST). Chapter 0305 will `type switch` on it.

---

## The Concept & Theory: Statement Dispatch & DDL vs DML

### SQL Verb Families

| Family | Examples | Effect |
| :--- | :--- | :--- |
| **DDL** (Data Definition) | `CREATE TABLE` | Changes catalog/schema metadata |
| **DML** (Data Manipulation) | `INSERT`/`UPDATE`/`DELETE` | Changes row data |
| **DQL** (Data Query) | `SELECT` | Reads data; no durable mutation |

Parsers usually share one entry point (`parseStmt`) that classifies the verb early. Executors then branch on AST type. Keeping parse and execute separate means you can unit-test parsing without opening a database.

### Recursive Descent & LL(k)

Our style is **recursive descent**: each grammar rule is roughly one function (`parseInsert`, `parseWhere`, …). For multi-word keywords (`CREATE TABLE`) we need more than one token of lookahead → LL(k) with explicit save/rollback of `pos`.

This is how many production SQL parsers begin (before or beside generated parsers). You trade generator complexity for clarity and control.

### Why `interface{}` AST Nodes?

Go lacks a sealed class hierarchy. Returning `interface{}` (or a small interface with no methods) lets `parseStmt` yield different concrete structs. The executor’s `type switch` is the moral equivalent of a visitor. Later you might introduce a `Statement` interface; the teaching point is polymorphism at the parse/execute boundary.

### Catalog Thinking Starts at CREATE

`StmtCreateTable` is not “just another statement.” It defines the **schema contract** future DML needs. Even before 0305 persists it, understand: without CREATE (or equivalent catalog load), typed Encode/Decode has nothing to obey.

---

## 1. AST Zoo (Skeleton Cards)

### CREATE TABLE

```sql
create table t (a string, b int64, primary key (b));
```

```text
StmtCreateTable
┌────────┬────────────────────────────────┐
│ table  │ "t"                            │
│ cols   │ [{a,TypeStr}, {b,TypeI64}]     │
│ pkey   │ ["b"]                          │
└────────┴────────────────────────────────┘
```

### INSERT

```sql
insert into t values (1, 'hi');
```

```text
StmtInsert
┌────────┬──────────────────────────────┐
│ table  │ "t"                          │
│ value  │ [Cell(1), Cell("hi")]        │
└────────┴──────────────────────────────┘
```

### UPDATE

```sql
update t set a=1, b=2 where c=3 and d=4;
```

```text
StmtUpdate
┌────────┬────────────────────────────────┐
│ table  │ "t"                            │
│ value  │ SET assignments (NamedCells)   │
│ keys   │ WHERE equalities (NamedCells)  │
└────────┴────────────────────────────────┘
```

### DELETE

```sql
delete from t where c=3 and d=4;
```

```text
StmtDelete{ table:"t", keys:[ c→3, d→4 ] }
```

---

## 2. Grammar Summary

```text
stmt ::= select | create | insert | update | delete

create ::= CREATE TABLE name '(' item (',' item)* ')' ';'
item   ::= name type | PRIMARY KEY '(' name (',' name)* ')'
type   ::= int64 | string

insert ::= INSERT INTO name VALUES '(' value (',' value)* ')' ';'
update ::= UPDATE name SET assign (',' assign)* WHERE preds ';'
delete ::= DELETE FROM name WHERE preds ';'
select ::= SELECT cols FROM name WHERE preds ';'
```

---

## 3. Variadic Keywords + Rollback

Multi-word keywords need lookahead with undo:

```text
tryKeyword("CREATE", "TABLE")
  saved = pos
  match CREATE? ──no──► rollback, return false
  match TABLE?  ──no──► rollback, return false
  success: pos advanced past both
```

```text
Input: "CREATE VIEW ..."
  CREATE ✓  TABLE ✗  → rollback → try next statement kind
```

---

## 4. Comma-List Combinator

Parenthesized lists share one pattern:

```text
'('  item  (',' item)*  ')'

Used for:
  CREATE TABLE ( ... )
  PRIMARY KEY ( ... )
  INSERT VALUES ( ... )
```

```text
( a string , b int64 , primary key (b) )
  └─item─┘   └─item─┘   └─────item─────┘
```

---

## 5. Dispatcher Decision Table

| Leading keywords | AST |
| :--- | :--- |
| `SELECT` | `*StmtSelect` |
| `CREATE TABLE` | `*StmtCreateTable` |
| `INSERT INTO` | `*StmtInsert` |
| `UPDATE` | `*StmtUpdate` |
| `DELETE FROM` | `*StmtDelete` |
| else | error |

---

## 6. Constraints (Still Point-Query World)

| Allowed | Not yet |
| :--- | :--- |
| Single-row PK WHERE | Range scans |
| Exact equality AND-chains | OR / expressions |
| Typed cols `int64`/`string` | More SQL types |

---

## Crucial Takeaways

* `parseStmt` is the LL(k) front door for all SQL verbs.
* Each statement becomes a small, typed AST card.
* Variadic `tryKeyword` + rollback enables multi-word phrases.
* Next: **execute** those cards against schemas + KV (0305).
