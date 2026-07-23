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
