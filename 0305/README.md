# Chapter 0305: Execute SQL — Closing the Loop

## Overview: Parser Meets Storage
0304 produces AST nodes. This chapter builds the **execution engine**: route each AST to a pipeline, validate against a schema, talk to `KV`, and always return a unified **`SQLResult`**.

```text
SQL text
   │
   ▼
parseStmt() ──► AST (interface{})
   │
   ▼
ExecStmt() ──► GetSchema → makeRow/makePKey → KV → SQLResult
```

```text
┌──────────────────────────────────────────────────────┐
│  DB                                                  │
│  ┌─────────────┐   ┌──────────────────────────────┐  │
│  │ tables map  │   │ KV (log + mem + CRC…)        │  │
│  │ (RAM cache) │   │ includes @schema_* metadata  │  │
│  └─────────────┘   └──────────────────────────────┘  │
└──────────────────────────────────────────────────────┘
```

---

## The Concept & Theory: Binding, Catalogs, and Execution

### The Missing Middle: Name → Physical

Parsing gives you strings like table `"link"` and column `"time"`. Storage wants byte keys and integer offsets. **Binding** (catalog lookup) connects them:

```text
SQL names ──GetSchema / lookupColumns──► Schema indices / types ──Encode*──► KV bytes
```

Without a catalog, the executor cannot know that `src` is PK column 1 or that `time` is `int64`. `GetSchema` is our catalog API; `@schema_*` keys are the durable catalog store.

### Why Persist Schemas in the Same KV?

Metadata is data. If schemas lived only in a sidecar file with weaker durability, you could recover rows whose types you no longer understand. Storing JSON schemas in the same checksummed, fsynced log means:
* reboot reloads table definitions,
* backup/copy of the KV file keeps data+meaning together,
* the engine remains a single storage universe.

(Production catalogs are more sophisticated — system tables, versions — but the idea rhymes.)

### Executor as Interpreter of ASTs

`ExecStmt` is an **interpreter**:
* switch on statement kind,
* validate against schema,
* call into the relational/KV APIs from 020x,
* package outcomes as `SQLResult`.

There is not yet a separate optimizer producing multiple plan alternatives. For PK point queries, the “plan” is obvious: one Get/SetEx/Del. When range scans arrive, planning becomes a real subject; the AST→executor seam stays the same.

### Read-Modify-Write Updates

SQL `UPDATE … SET non_key = ? WHERE pk = ?` cannot rewrite only one column on disk unless the file format has per-column slots. Our value blob is a contiguous encoding of all non-PK cells. Therefore execution:
1. reads the full row,
2. patches fields in RAM,
3. writes the full value back.

That RMW pattern is ubiquitous. PK immutability follows: changing the key would change the row’s address — conceptually a delete plus insert, which we refuse in `fillNonPKey` for safety/clarity.

### The Vertical Slice Milestone

With 0305 complete, the project finally demonstrates an end-to-end database loop:

> text SQL → parse → bind → durable typed storage → typed result

Everything before was a necessary organ; this chapter is the first time the organism walks. Later chapters deepen organs (ordering, indexes, scans) without throwing away this loop.

---

## 1. Unified Result Shape

```go
type SQLResult struct {
    Updated int      // rows mutated (DML)
    Header  []string // column names (SELECT)
    Values  []Row    // result rows (SELECT)
}
```

| Statement | Typical `SQLResult` |
| :--- | :--- |
| `CREATE TABLE` | `Updated=0`, empty values (success = no error) |
| `INSERT/UPDATE/DELETE` | `Updated=0 or 1` |
| `SELECT` | `Header` + `Values` (0 or 1 row for PK point query) |

---

## 2. Schema Registry (Two Tiers)

```text
GetSchema("link")
        │
        ▼
  hit RAM tables map? ──yes──► return
        │ no
        ▼
  KV.Get("@schema_link") ── JSON Unmarshal ──► cache in RAM ──► return
```

### Mock KV metadata entry

```text
Key:   @schema_link
Value: {"Table":"link","Cols":[...],"PKey":[1,2]}
```

```text
myKV.mem (conceptual):
┌──────────────────┬────────────────────────────────┐
│ @schema_link     │ JSON schema bytes              │
│ link\0bob\0alice │ row value bytes (time=…)       │
└──────────────────┴────────────────────────────────┘
```

Schemas survive `Close`/`Open` because they live in the durable KV log like any other key.

---

## 3. Execution Pipelines

### DDL — `CREATE TABLE`

```text
StmtCreateTable
   │
   ├─ build Schema{Cols, PKey indices}
   ├─ json.Marshal → KV.Set("@schema_"+table)
   └─ db.tables[table] = schema
```

### DML — `INSERT`

```text
values (123, 'bob', 'alice')
   │
   ▼
makeRow / align to columns
   │
   ▼
EncodeKey + EncodeVal
   │
   ▼
SetEx(..., ModeInsert) → Updated=1 or 0
```

### DML — `UPDATE` (Read-Modify-Write)

```text
WHERE src='bob' AND dst='alice'
   │
   ▼
makePKey → Get existing row
   │
   ▼
apply SET columns in RAM
   │  (reject if assignment targets a PK column)
   ▼
SetEx(..., ModeUpdate)
```

```text
PK immutability:
  UPDATE … SET src='x' …  → ERROR
  (PK is the physical address; change = delete+insert)
```

### DML — `DELETE`

```text
WHERE … → makePKey → EncodeKey → Del → Updated=0|1
(tombstone in log under the hood)
```

### DQL — `SELECT`

```text
SELECT time FROM link WHERE dst='alice' AND src='bob';
   │
   ├─ makePKey from WHERE
   ├─ Get + DecodeVal
   └─ subsetRow → only ["time"]
        │
        ▼
SQLResult{
  Header: ["time"],
  Values: [ [ Cell{I64:123} ] ],
}
```

---

## 4. End-to-End Mock (From Tests)

```text
① CREATE TABLE link (
     time int64, src string, dst string,
     primary key (src, dst)
   );
   → schema cached + persisted

② INSERT INTO link VALUES (123, 'bob', 'alice');
   → Updated: 1

   Physical sketch:
   key: link \0 Enc("bob") Enc("alice")
   val: Enc(123)

③ SELECT time FROM link WHERE dst='alice' AND src='bob';
   → Header:[time]  Values:[[123]]

④ UPDATE link SET time=456 WHERE dst='alice' AND src='bob';
   → Updated: 1
   → re-SELECT → Values:[[456]]

⑤ Close / Open  (schema reloads from @schema_link)

⑥ DELETE FROM link WHERE src='bob' AND dst='alice';
   → Updated: 1
   → SELECT → empty Values
```

### Timeline table

| Step | Op | `Updated` / rows |
| :---: | :--- | :--- |
| 1 | CREATE | schema ready |
| 2 | INSERT | 1 |
| 3 | SELECT time | 1 row: 123 |
| 4 | UPDATE time=456 | 1 |
| 5 | SELECT | 1 row: 456 |
| 6 | DELETE | 1 |
| 7 | SELECT | 0 rows |

---

## 5. Helper Function Map

| Helper | Job |
| :--- | :--- |
| `lookupColumns` | name → column index |
| `makePKey` | WHERE `NamedCell`s → PK-shaped `Row` |
| `makeRow` | INSERT values → full `Row` |
| `subsetRow` | projection for SELECT header |
| `fillNonPKey` | apply UPDATE assignments; block PK writes |
| `ExecStmt` | type-switch router |

```text
ExecStmt switch:
  *StmtCreateTable → execCreateTable
  *StmtSelect      → execSelect
  *StmtInsert      → execInsert
  *StmtUpdate      → execUpdate
  *StmtDelete      → execDelete
```

---

## 6. System Constraints (Still)

| Constraint | Reason |
| :--- | :--- |
| WHERE = exact PK match | No range iterators yet |
| Single-row DML | Point updates only |
| PK immutable on UPDATE | Key is physical address |
| Types: `int64`, `string` | Matches Cell layer |

---

## Big Picture: 0101 → 0305

```text
0101  In-memory KV API
0102  Serialize Entry
0103  Append-only log
0104  fsync durability
0105  CRC atomicity
0201  Typed Cell
0202  Schema / Row ↔ KV key-val
0203  Insert/Update/Upsert modes
0204  DB CRUD façade
0301  Lexer
0302  Literal values
0303  SELECT AST
0304  All statement ASTs
0305  ExecStmt + schema registry   ← you are here
```

You now have a vertical slice: **SQL string → durable typed row** — limited, but real.

---

## Crucial Takeaways

* `ExecStmt` is the traffic controller; `SQLResult` is the universal reply.
* Schemas are data too: `@schema_*` JSON in KV + RAM cache.
* UPDATE is read-modify-write with PK write-protection.
* SELECT projects columns after a point Get — not a table scan.
* Next chapters grow ordering, indexes, and richer query plans on this foundation.
