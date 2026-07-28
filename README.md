# db_engine — Course Map

A chapter-by-chapter build of a database engine in Go. Each folder (`0101/`, `0204/`, `0305/`, …) is a self-contained snapshot you can `cd` into and `go test -v`.

This document is the **index**: what each chapter owns, which operations exist at each layer, and how SQL flows down to bytes on disk.

---

## Architecture: Four Layers

```text
┌─────────────────────────────────────────────────────────────────┐
│  03xx  SQL text  →  tokens  →  AST  →  ExecStmt  →  SQLResult   │  language
├─────────────────────────────────────────────────────────────────┤
│  02xx  Schema / Row / DB.Insert|Select|Update|Delete            │  relational
├─────────────────────────────────────────────────────────────────┤
│  04xx  sorted keys, Seek, iterators, order-preserving encoding  │  ordered access
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

**Milestone:** walk rows in key order without random `Get` per guess.

---

## Operations by Layer

### Storage (`KV`) — 01xx + 0401–0402

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

### Relational (`DB`) — 02xx + 0404

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

### SQL — 03xx

| Operation | What it does | Introduced | Used when |
|-----------|--------------|------------|-----------|
| `tryName` / lexer | Tokenize identifiers | **0301** | Every SQL string |
| `parseValue` | Parse `123`, `'bob'` | **0302** | Literals in SQL |
| `parseSelect` → `StmtSelect` | AST for PK `SELECT` | **0303** | Point-query SELECT |
| `parseStmt` | CREATE / INSERT / UPDATE / DELETE too | **0304** | Full SQL surface |
| `ExecStmt` | Run AST against `DB` | **0305** | End-to-end SQL |

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

### `SELECT * FROM link WHERE src > 'bob'` (future, 04xx)

```text
0404  RowIterator over table prefix
0403  order-preserving EncodeKey (byte order = logical order)
0402  KVIterator.Seek + Next
0401  sorted keys array
```

Through **0305**, every SQL statement still ends in **`Get`**, **`SetEx`**, or **`Del`** — all point lookups. Range scans and full table scans arrive in **04xx**.

---

## When Is Each Operation Used?

| User action | SQL (03xx) | DB (02xx) | KV (01xx / 04xx) |
|-------------|------------|-----------|------------------|
| Read one row by PK | `ExecStmt(SELECT)` | `Select` → `Get` | `Get` |
| Insert row | `ExecStmt(INSERT)` | `Insert` | `SetEx(Insert)` |
| Change row | `ExecStmt(UPDATE)` | `Update` | `Get` + `SetEx(Update)` |
| Remove row | `ExecStmt(DELETE)` | `Delete` | `Del` |
| Define table | `CREATE TABLE` | store schema in KV | `Set` on `@schema_*` key |
| Scan many rows | (later SQL) | `RowIterator` (0404) | `Seek` + `Next` (0402) |
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
0401 → 0404     ordering + scans
```

### Already at 0305? Fill gaps, then continue 04xx

1. **0101 + 0103 + 0105** — how `Get` / `Set` / `Del` and `Open()` actually work
2. **0202 + 0203 + 0204** — how `Insert` / `Select` / `Update` map to KV
3. **0301–0305** — parser + executor (likely mostly done)
4. **0401 onward** — new material: order, iterators, scans

### Only care about “when does SQL hit disk?”

Start at **0305** (`ExecStmt`), then drill down on demand:

- `ExecStmt` → **0204** (`db.Insert`, etc.)
- `db.Insert` → **0202** (`EncodeKey`)
- `db.Insert` → **0203** (`SetEx`)
- `SetEx` → **0103–0105** (log + fsync + CRC)
- Scans → **0401–0404**

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

Run tests in any chapter:

```bash
cd 0305 && go test -v
```

---

## Running the Course

Each chapter folder is an independent Go package (`package kv`). Work through chapters in order; later folders copy and extend earlier code. When confused about *where* a function lives, use this map — when confused about *how* it works, read that chapter's `README.md`.
