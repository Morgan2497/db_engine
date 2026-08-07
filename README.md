# db_engine — Course Map

A chapter-by-chapter build of a database engine in Go. Each folder (`0101/`, `0204/`, `0305/`, `0405/`, `0501/`, …) is a self-contained snapshot you can `cd` into and `go test -v`.

This document is the **index**: what each chapter owns, which operations exist at each layer, and how SQL flows down to bytes on disk.

---

## Architecture: Five Layers

```text
┌─────────────────────────────────────────────────────────────────┐
│  05xx  expressions → trees → precedence → later evaluation      │  expressions
├─────────────────────────────────────────────────────────────────┤
│  03xx  SQL text → statement AST → ExecStmt → SQLResult           │  language
├─────────────────────────────────────────────────────────────────┤
│  02xx  Schema / Row / DB.Insert|Select|Update|Delete            │  relational
├─────────────────────────────────────────────────────────────────┤
│  04xx  sorted keys, bounded ranges, order-preserving encoding    │  ordered access
├─────────────────────────────────────────────────────────────────┤
│  01xx  KV.Get / Set / Del / log / fsync / CRC / Open replay     │  storage
└─────────────────────────────────────────────────────────────────┘
```

**Rule:** upper layers call lower layers. SQL never talks to disk directly — it goes through `DB` → `Row.EncodeKey` → `KV`.

---

## Chapter Index

### 01xx — Storage: “Can bytes survive a crash?”

| Ch | Responsibility | New operations |
|----|----------------|----------------|
| **0101** | In-memory KV map | `Get`, `Set`, `Del` |
| **0102** | Wire format for log entries | `Entry.Encode` / `Decode` |
| **0103** | Append-only log + replay → rebuild `mem` | `Open`, `log.Write` / `Read` |
| **0104** | Real durability (`fsync`) | `log.Sync` |
| **0105** | CRC, ignore torn last record | safe `Open()` replay |

**Milestone:** durable `Get` / `Set` / `Del` on raw `[]byte` keys.

### 02xx — Relational: “How do tables live on a dumb KV?”

| Ch | Responsibility | New operations |
|----|----------------|----------------|
| **0201** | Typed `Cell` | encode/decode one column |
| **0202** | `Schema`, `Row`, `EncodeKey` / `EncodeVal` | table → bytes |
| **0203** | Write semantics | `SetEx`, `ModeInsert` / `ModeUpdate` / `ModeUpsert` |
| **0204** | Relational façade | `DB.Insert` / `Select` / `Update` / `Delete` |

**Milestone:** CRUD on rows without SQL (`TestTableByPKey`).

### 03xx — SQL: “How does SQL text become storage calls?”

| Ch | Responsibility | New operations |
|----|----------------|----------------|
| **0301** | Lexer (identifiers) | `tryName`, cursor / `pos` |
| **0302** | Literals | `parseInt`, `parseString`, `parseValue` |
| **0303** | First AST | `parseSelect` → `StmtSelect` |
| **0304** | All statements | `parseStmt`, CREATE / INSERT / UPDATE / DELETE ASTs |
| **0305** | Executor | `ExecStmt`, `GetSchema`, `SQLResult` |

**Milestone:** full SQL loop (`TestSQLByPKey`).

### 04xx — Ordering: “How do we scan, not just point-lookup?”

| Ch | Responsibility | New operations |
|----|----------------|----------------|
| **0401** | Replace hash map with **sorted arrays** | same `Get` / `Set` / `Del`, new `Open` compaction |
| **0402** | Iterator abstraction | `Seek`, `KVIterator.Next` / `Prev` |
| **0403** | Sort-correct encoding | order-preserving `EncodeKey` for ints / strings |
| **0404** | Table-scoped row scan | `DB.Seek`, `RowIterator` |
| **0405** | Inclusive bounded and prefix ranges | `RangeReq`, `EncodeKeyPrefix`, `KV.Range`, `RangedKVIter` |

**Milestone:** walk rows in either direction inside encoded start/stop boundaries, including complete composite-key prefix groups.

### 05xx — Expressions: “How does flat SQL become a computation tree?”

| Ch | Responsibility | New operations |
|----|----------------|----------------|
| **0501** | Infix expressions and left-associative trees | `ExprBinOp`, `parseAtom`, `parseAdd` |

**Milestone:** parse `a - b + c - e` into the explicit tree `(((a - b) + c) - e)`.

Chapter 0501 deliberately builds only the tree. Evaluation, additional
precedence levels, parentheses, and the complete SQL operator set arrive in
later 05xx chapters. See the [detailed Chapter 0501 notes](0501/README.md).

---

## Operations by Layer

### Storage (`KV`) — 01xx + 0401–0405

| Operation | What it does | Introduced | Used when |
|-----------|--------------|------------|-----------|
| `Set` | Upsert key → value | **0101** | Always overwrite or insert |
| `Get` | Point lookup by exact key | **0101** | Read one key |
| `Del` | Remove key (tombstone) | **0101** | Delete one key |
| `Open` / `Close` | Start/stop, replay log | **0103** | Every DB session |
| `log.Write` | Append change to disk | **0103** | Every durable write |
| `fsync` (`Sync`) | Force bytes to disk | **0104** | After each log write |
| CRC / torn-write detect | Safe replay | **0105** | On `Open()` replay |
| `SetEx` + modes | Insert-only / update-only / upsert | **0203** | SQL `INSERT` vs `UPDATE` semantics |
| Sorted `keys[]` / `vals[]` | Ordered storage | **0401** | Foundation for scans |
| `Seek` + `KVIterator` | Position at key, walk forward/back | **0402** | Range queries, table scans |
| `KV.Range` + `RangedKVIter` | Enforce an inclusive stop key in either direction | **0405** | Bounded ascending/descending scans |

### Relational (`DB`) — 02xx + 0404–0405

| Operation | What it does | Introduced | Used when |
|-----------|--------------|------------|-----------|
| `Cell` encode/decode | Typed value ↔ bytes | **0201** | Every column |
| `EncodeKey` / `EncodeVal` | Row → KV key + value | **0202** | Every row read/write |
| `Insert` | New row by PK | **0204** | `INSERT INTO` |
| `Select` | Read row by PK | **0204** | `SELECT … WHERE pk = …` |
| `Update` | Change non-PK columns | **0204** | `UPDATE … WHERE pk = …` |
| `Delete` | Remove row by PK | **0204** | `DELETE … WHERE pk = …` |
| `GetSchema` | Load table definition | **0305** | Before any SQL exec |
| `DB.Seek` / `RowIterator` | Scan rows in key order for one table | **0404** | `SELECT *`, range filters |
| `DB.Range` / `RangeReq` | Convert logical comparisons to encoded range bounds | **0405** | `<`, `<=`, `>`, `>=`, prefix scans |
| `EncodeKeyPrefix` | Place a synthetic boundary before or after a prefix group | **0405** | Inclusive/strict composite-key bounds |

### SQL and expressions — 03xx + 0501

| Operation | What it does | Introduced | Used when |
|-----------|--------------|------------|-----------|
| `tryName` / lexer | Tokenize identifiers | **0301** | Every SQL string |
| `parseValue` | Parse `123`, `'bob'` | **0302** | Literals in SQL |
| `parseSelect` → `StmtSelect` | AST for PK `SELECT` | **0303** | Point-query SELECT |
| `parseStmt` | CREATE / INSERT / UPDATE / DELETE too | **0304** | Full SQL surface |
| `ExecStmt` | Run AST against `DB` | **0305** | End-to-end SQL |
| `ExprBinOp` | Store an operator with left/right child expressions | **0501** | Expression tree nodes |
| `parseAtom` | Parse a column name or literal leaf | **0501** | Leaves of an expression tree |
| `parseAdd` | Build a left-associative `+`/`-` tree | **0501** | Basic infix expressions |

---

## Three User-Facing APIs

```text
API level          Example call                      Chapters
─────────────────  ────────────────────────────────  ─────────────────────
SQL                db.ExecStmt("INSERT INTO …")      0301–0305
Relational         db.Insert(schema, row)            0201–0204
Raw storage        kv.SetEx(key, val, ModeInsert)    0101–0105, 0401–0402
```

Same `INSERT` at the bottom is always: **encode row → `SetEx` → log write → update mem**.

---

## SQL → Storage Call Chains

Example schema: `link(time int64, src string, dst string, primary key (src, dst))`.

### `INSERT INTO link VALUES (123, 'bob', 'alice')`

```text
0304  parseStmt           → StmtInsert AST
0305  execInsert          → build Row from literals
0204  db.Insert           → EncodeKey + EncodeVal
0203  kv.SetEx(..., ModeInsert)
0103  log.Write + replay path
0104  fsync
0401  binary search + slices.Insert (if new key)
```

### `SELECT time FROM link WHERE src = 'bob' AND dst = 'alice'`

```text
0303/0304  parseSelect    → StmtSelect (PK columns in WHERE)
0305       execSelect     → makePKey from WHERE
0204       db.Select      → EncodeKey(partial row) + Get
0202       DecodeVal      → fill Row from bytes
0101/0401  kv.Get         → point lookup
```

### `UPDATE link SET time = 456 WHERE src = 'bob' AND dst = 'alice'`

```text
0305  execUpdate  → read-modify-write
0204  db.Update   → Select (Get) then SetEx(ModeUpdate)
0203  SetEx ModeUpdate  → no-op if PK missing or value unchanged
```

### `DELETE FROM link WHERE src = 'bob' AND dst = 'alice'`

```text
0305  execDelete
0204  db.Delete   → EncodeKey + Del
0101  kv.Del      → tombstone + remove from mem
```

### Bounded row range (0405 relational API)

```text
0405  RangeReq comparisons → synthetic ±∞ suffixes
0405  EncodeKeyPrefix → encoded start/stop keys
0405  DB.Range → RowIterator
0405  KV.Range → RangedKVIter.Valid + Next
0403  order-preserving key bytes
0402  KVIterator.Seek + Next/Prev
0401  sorted keys array
```

The bounded range machinery exists in 0405, while SQL range-expression
integration is developed through the expression chapters. Chapter 0501 first
turns simple infix input into a tree; it does not evaluate that tree yet.

### `a - b + c - e` (0501 expression parsing)

```text
0501  parseAdd
        ↓ parseAtom leaves from left to right
      a, b, c, e
        ↓ wrap the expression constructed so far
      (((a - b) + c) - e)
        ↓ in-memory tree
            -
           / \
          +   e
         / \
        -   c
       / \
      a   b
```

---

## When Is Each Operation Used?

| User action | SQL (03xx) | DB (02xx) | KV (01xx / 04xx) |
|-------------|------------|-----------|------------------|
| Read one row by PK | `ExecStmt(SELECT)` | `Select` → `Get` | `Get` |
| Insert row | `ExecStmt(INSERT)` | `Insert` | `SetEx(Insert)` |
| Change row | `ExecStmt(UPDATE)` | `Update` | `Get` + `SetEx(Update)` |
| Remove row | `ExecStmt(DELETE)` | `Delete` | `Del` |
| Define table | `CREATE TABLE` | store schema in KV | `Set` on `@schema_*` key |
| Scan many rows | range expressions are being developed in 05xx | `DB.Range` / `RowIterator` (0405) | `Range` + `Next`/`Prev` (0405) |
| Parse arithmetic structure | `parseAdd` (0501) | — | — |
| Survive restart | — | `db.Open` | `kv.Open` replay log |

---

## Study Order

### Full bottom-up path (recommended for first pass)

```text
0101 → 0105     storage kernel (Get / Set / Del + durability)
  ↓
0201 → 0204     rows + DB CRUD
  ↓
0301 → 0305     SQL compiler + executor
  ↓
0401 → 0405     ordering + bounded prefix scans
  ↓
0501 onward     expression trees, evaluation, precedence
```

### Already at 0305? Fill gaps, then continue 04xx

1. **0101 + 0103 + 0105** — how `Get` / `Set` / `Del` and `Open()` actually work
2. **0202 + 0203 + 0204** — how `Insert` / `Select` / `Update` map to KV
3. **0301–0305** — parser + executor (likely mostly done)
4. **0401–0405** — order, iterators, bounded and prefix scans
5. **0501 onward** — expression trees, evaluation, and precedence

### Only care about “when does SQL hit disk?”

Start at **0305** (`ExecStmt`), then drill down on demand:

- `ExecStmt` → **0204** (`db.Insert`, etc.)
- `db.Insert` → **0202** (`EncodeKey`)
- `db.Insert` → **0203** (`SetEx`)
- `SetEx` → **0103–0105** (log + fsync + CRC)
- Scans → **0401–0405**
- Expression parsing → **0501** (`parseAtom`, `parseAdd`, `ExprBinOp`)

---

## Milestone Tests

| Test | Chapter | Proves |
|------|---------|--------|
| `TestKVBasic` | 0101+ | Get / Set / Del |
| `TestKVRecovery` | 0103+ | Log replay after reopen |
| `TestBadChecksum` / `TestTornWriteRecovery` | 0105 | Safe replay |
| `TestKVUpdateMode` | 0203+ | Insert / Update / Upsert modes |
| `TestTableByPKey` | 0204+ | Relational CRUD without SQL |
| `TestSQLByPKey` | 0305+ | Full SQL + reopen |
| `TestKVSeek` | 0402+ | Iterator positioning |
| `TestIterByPKey` | 0404+ | Table-scoped row scan |
| `TestKVRangedAscending` / `TestKVRangedDescending` | 0405 | Inclusive bounded scans in both directions |
| `TestParseAtom` | 0501 | Column and literal expression leaves |
| `TestParseAddBuildsLeftAssociativeTree` | 0501 | Nested left-associative `+`/`-` tree |

Run tests in any chapter:

```bash
cd 0305 && go test -v
```

To print Chapter 0501's expression tree:

```bash
go test -v ./0501 -run TestParseAddBuildsLeftAssociativeTree
```

---

## Running the Course

Each chapter folder is an independent Go package (`package kv`). Work through chapters in order; later folders copy and extend earlier code. When confused about *where* a function lives, use this map — when confused about *how* it works, read that chapter's `README.md`.
