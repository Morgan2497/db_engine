# Chapter 0204: CRUD — The Relational `DB` Wrapper

## Overview: Stop Hand-Encoding Rows
0202 taught `EncodeKey`/`EncodeVal`. 0203 taught conditional `SetEx`. Application code still shouldn’t juggle those by hand.

This chapter introduces **`DB`**: a thin relational façade over `KV` that speaks **schema + row** instead of raw bytes.

```text
┌─────────────────────────────────────────────┐
│  DB  Select / Insert / Upsert / Update / Delete │
│         │                                   │
│         ▼                                   │
│  Row.EncodeKey / EncodeVal / DecodeVal      │
│         │                                   │
│         ▼                                   │
│  KV.Get / SetEx / Del  (+ durable log)      │
└─────────────────────────────────────────────┘
```

---

## The Concept & Theory: Façades and Layering

### Why Introduce `DB` at All?

You could call `EncodeKey` + `SetEx` from application code forever. That leaks storage concerns into every caller. The `DB` type is a **façade**: a narrow, intention-revealing API (`Insert`, `Select`, …) that hides byte packing and update modes.

This matches how real drivers feel:
* Users think in rows and statements.
* Drivers/engines translate to pages, keys, and slots.

### What “Leaking Storage Details” Means

This is **not** a security issue. It is a **layering** issue: when application code talks directly to `KV` + `EncodeKey`/`EncodeVal`, it must know **how rows are physically stored**, not just **what row it wants**.

In our engine, a logical row is split on purpose:

```text
Logical row:  (time=123, src="a", dst="b")

Physical KV:
  key = "link\0" + enc(src) + enc(dst)   ← PK columns only
  val = enc(time)                         ← non-PK columns only
```

That split is a storage decision. SQL users do not think in `key`/`val` blobs. If callers encode by hand, every caller inherits these details:

| Detail | Caller must know |
| :--- | :--- |
| Key format | `table name + 0x00 + PK cells` |
| Value format | only non-PK columns, in schema column order |
| Cell encoding | i64 = 8 little-endian bytes; str = 4-byte length + bytes |
| PK vs non-PK | `schema.PKey` indices — which columns go where |
| Write semantics | `ModeInsert` vs `ModeUpdate` vs `ModeUpsert` |
| Read semantics | `Get(key)` then `DecodeVal` — not “give me this row” |
| Delete semantics | PK-only row → `EncodeKey` → `Del` |

`DB` hides that behind `Insert(schema, row)` / `Select(schema, row)`.

#### Without `DB` — application code does query execution by hand

Using the `link` table (`PKey = [1, 2]` → src, dst):

```go
func SaveLink(kv *KV, schema *Schema, time int64, src, dst string) error {
    row := Row{
        {Type: TypeI64, I64: time},
        {Type: TypeStr, Str: []byte(src)},
        {Type: TypeStr, Str: []byte(dst)},
    }
    key := row.EncodeKey(schema)
    val := row.EncodeVal(schema)
    updated, err := kv.SetEx(key, val, ModeInsert)
    if err != nil {
        return err
    }
    if !updated {
        return errors.New("duplicate link") // caller interprets KV bool
    }
    return nil
}

func LoadLink(kv *KV, schema *Schema, src, dst string) (time int64, ok bool, err error) {
    row := Row{
        {}, // time empty — PK lookup only needs src/dst filled
        {Type: TypeStr, Str: []byte(src)},
        {Type: TypeStr, Str: []byte(dst)},
    }
    key := row.EncodeKey(schema)
    val, found, err := kv.Get(key)
    if err != nil || !found {
        return 0, found, err
    }
    if err = row.DecodeVal(schema, val); err != nil {
        return 0, false, err
    }
    return row[0].I64, true, nil
}
```

Every caller must know: build a full `Row`, call `EncodeKey` **and** `EncodeVal`, pick the right `UpdateMode`, and choreograph `Get` + `DecodeVal` on reads.

#### With `DB` — same operations, storage-agnostic intent

```go
func SaveLink(db *DB, schema *Schema, time int64, src, dst string) (bool, error) {
    row := Row{
        {Type: TypeI64, I64: time},
        {Type: TypeStr, Str: []byte(src)},
        {Type: TypeStr, Str: []byte(dst)},
    }
    return db.Insert(schema, row)
}

func LoadLink(db *DB, schema *Schema, src, dst string) (time int64, ok bool, err error) {
    row := Row{
        {},
        {Type: TypeStr, Str: []byte(src)},
        {Type: TypeStr, Str: []byte(dst)},
    }
    ok, err = db.Select(schema, row)
    if !ok || err != nil {
        return 0, ok, err
    }
    return row[0].I64, true, nil
}
```

Still schema/row knowledge — but no key layout, no modes, no `Get`+`DecodeVal` dance.

#### Why the coupling matters later

**1. Encoding changes break every caller**

If you later add a version byte to keys:

```text
OLD: "link\0" + enc(src) + enc(dst)
NEW: "link\0" + 0x01 + enc(src) + enc(dst)
```

| With `DB` | Without `DB` |
| :--- | :--- |
| Fix `EncodeKey` in one place | Fix every `SaveLink`, `LoadLink`, test helper, CLI… |

The façade is a **stable seam**.

**2. Backend swap**

Today: in-memory map + log. Later: B-tree pages, WAL records, replication. Application code should call `db.Select`, not `kv.mem[string(key)]`.

**3. SQL executor (0305) stays clean**

```text
parse INSERT → build Row → db.Insert(schema, row)
```

Not:

```text
parse INSERT → build Row → EncodeKey → EncodeVal → SetEx(ModeInsert)
```

The parser/executor should speak **operations** (`Insert`, `Update`, `Select`), not **bytes**.

**4. Wrong abstraction at the wrong layer**

```go
// KV-level — storage internals leak upward:
updated, err := kv.SetEx(key, val, ModeInsert)
if !updated { return ErrPKCollision }

// Relational-level — intent is explicit:
updated, err := db.Insert(schema, row)
// later: SQL layer maps updated → "1 row inserted" / constraint error
```

`updated bool` is a KV concept. “0 rows affected” / “duplicate key” are relational concepts. `DB` is where that translation belongs.

**5. Easy footguns when details leak**

| Mistake | Effect |
| :--- | :--- |
| `Set` instead of `SetEx(..., ModeInsert)` on INSERT | silent overwrite (upsert) |
| `EncodeVal` for a Select | wrong — you need `Get` + `DecodeVal` |
| Put a PK column in `val` by hand | wrong layout / double storage |
| Forget `table\0` prefix | collision between tables |
| `ModeUpdate` when row is missing | silent no-op instead of SQL error |

`DB.Insert` / `DB.Update` / `DB.Select` make the **intent** explicit.

#### Who knows what — layered responsibilities

```text
WITHOUT DB (leaky):
┌─────────────────────────────────────┐
│  Application / future SQL executor   │
│  knows: key layout, val layout,      │
│         modes, Get/DecodeVal dance    │
└─────────────────┬───────────────────┘
                  ▼
              KV + Row encoding

WITH DB (layered):
┌─────────────────────────────────────┐
│  Application / SQL executor          │
│  knows: schema, row, Insert/Select   │
└─────────────────┬───────────────────┘
                  ▼
┌─────────────────────────────────────┐
│  DB                                  │
│  knows: encode/decode, modes         │
└─────────────────┬───────────────────┘
                  ▼
┌─────────────────────────────────────┐
│  KV                                  │
│  knows: bytes, log, mem              │
└─────────────────────────────────────┘
```

`DB` in 0204 is thin (~40 lines), but the **pattern** scales: 0204 hides encode + modes; 0305 hides schema lookup; later chapters hide scans and indexes. Each layer pushes byte/page details one step down.

### Point-Lookup Relational Algebra (Tiny Subset)

With only PK access, our relational engine supports a *subset* of what SQL usually implies:
* **Exact fetch by identity** (clustered primary key lookup)
* **Projection** will appear fully once SQL SELECT lists arrive (0305)
* **No scans, joins, or filters on non-key columns** yet

That is not a failure — it is staged complexity. Many production plans *start* with a PK lookup when the optimizer can prove it. We implement that happy path first.

### Partial Rows Are Normal

`Select`/`Delete` often populate **only PK fields** in the `Row`. Non-key slots are empty until `DecodeVal` fills them. That mirrors SQL: `WHERE id=5` does not require you to supply the other columns. Mentally treat a `Row` as a **buffer of slots** keyed by schema index, not always a fully hydrated tuple.

### Idempotency at the Relational Edge

Because `SetEx`/`Del` report whether state changed, `DB` operations can later expose SQL-like row counts (`Updated=1` vs `0`) without inventing new storage semantics. Chapter 0305’s `SQLResult.Updated` sits on this foundation.

---

## 1. The `DB` Type

```go
type DB struct {
    KV KV
}
```

| Method | Row input | KV call |
| :--- | :--- | :--- |
| `Select(schema, row)` | PK columns populated | `Get` → `DecodeVal` |
| `Insert(schema, row)` | All columns | `SetEx(..., ModeInsert)` |
| `Upsert(schema, row)` | All columns | `SetEx(..., ModeUpsert)` |
| `Update(schema, row)` | All columns | `SetEx(..., ModeUpdate)` |
| `Delete(schema, row)` | PK columns | `Del` |

---

## 2. Select Flow (Skeleton)

Wallet-style schema: `(wallet_id PK, owner_name, balance)`

```text
Input row (partial):
┌────────────┬────────────┬─────────┐
│ wallet_id  │ owner_name │ balance │
│ 999        │  (empty)   │ (empty) │
└────────────┴────────────┴─────────┘

1. key = EncodeKey(schema, row)     →  "wallets\0" + Enc(999)
2. val, ok = KV.Get(key)
3. if ok: DecodeVal into same row   → fills name + balance

Output row:
┌────────────┬────────────┬─────────┐
│ 999        │ "Morgan"   │ 1500    │
└────────────┴────────────┴─────────┘
```

`DB` never hard-codes byte offsets — schema + `Row` helpers own the layout.

---

## 3. Insert / Upsert / Update Visualization

```text
Full row for INSERT:
┌──────┬────────┬─────────┐
│ 999  │ Morgan │ 1500    │
└──────┴────────┴─────────┘
        │
        ├── EncodeKey → KV key
        └── EncodeVal → KV val
                │
                ▼
         SetEx(key, val, ModeInsert)
```

| Op | Existing PK | Result |
| :--- | :---: | :--- |
| Insert | no | write, `updated=true` |
| Insert | yes | no-op, `updated=false` (PK collision) |
| Update | no | no-op, `updated=false` |
| Update | yes | overwrite value |
| Upsert | either | insert or overwrite |

---

## 4. Delete

```text
Delete needs PK only:
┌──────┐
│ 999  │  → EncodeKey → Del(key)
└──────┘

Missing key → deleted=false (idempotent)
```

---

## 5. What Is Still Out of Scope

| Not yet | Why |
| :--- | :--- |
| SQL strings | Parser arrives in 0301+ |
| Table registry / `CREATE TABLE` | 0304/0305 |
| Range scans / secondary indexes | Later chapters |
| Changing a PK in place | Would be delete+insert; blocked later in exec |

This chapter is **point-lookup CRUD by primary key** on a hand-built `Schema`.

---

## Crucial Takeaways

* `DB` is the relational API; `KV` remains the byte basement.
* Select hydrates non-PK columns via `DecodeVal`.
* Insert/Update/Upsert are thin wrappers around `SetEx` modes.
* Next arc: **parse SQL** into structures this layer can execute (0301–0305).
