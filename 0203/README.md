# Chapter 0203: Update Modes (`SetEx`)

## Overview: SQL Write Semantics ≠ Blind Overwrite
The KV `Set` from earlier chapters always upserts: insert if missing, overwrite if present. SQL is stricter:

| SQL | Required behavior |
| :--- | :--- |
| `INSERT` | Fail / no-op if PK **already exists** |
| `UPDATE` | Fail / no-op if PK **does not exist** |
| Upsert (`INSERT … ON CONFLICT`) | Insert or overwrite |

If the SQL layer did `Get` then `Set`, every write would need **two** tree/map lookups and race under concurrency. This chapter pushes the decision into one call: **`SetEx(key, val, mode)`**.

```text
SQL layer                         KV layer
─────────                         ────────
INSERT  ──ModeInsert──►  SetEx ──► one existence check + conditional write
UPDATE  ──ModeUpdate──►  SetEx
UPSERT  ──ModeUpsert──►  SetEx
```

---

## The Concept & Theory: Semantics Belong Near the Data

### SQL Is Not “Just CRUD Bytes”

From a naïve storage view, every write is an overwrite. From a SQL view, writes carry **intent**:
* `INSERT` asserts “this identity must be new.”
* `UPDATE` asserts “this identity must already exist.”
* Upsert asserts “make it so, regardless.”

If the storage API only offers blind `Set`, the SQL layer must invent those semantics with a read-modify-write cycle. That is not only slower — it is **racy** once you have concurrent writers: two Inserts can both `Get` miss, then both `Set`, and you silently lose uniqueness.

### Push Predicates Down (A Database Motif)

A recurring database systems theme: **evaluate conditions as close to the data as possible**.
* Query predicates push into index range scans.
* Storage engines expose conditional puts (`compare-and-swap`, `put-if-absent`).

`SetEx` is our tiny version of that motif: existence checks happen at the map/leaf in **one** traversal, with a mode flag describing SQL intent.

### Return Values as Observable Semantics

`updated bool` is part of the semantic contract:
* Insert on conflict → `false` (caller / SQL can turn this into an error or “0 rows”).
* Update on miss → `false`.
* Upsert with identical bytes → `false` (idempotent; may skip logging).

Engines carefully define whether “no-op success” is an error or a zero row count. Teaching `bool` early forces you to think about that distinction before SQL error codes arrive.

### Preparing for Trees and LSM Leaves

Today the “one traversal” benefit is a map lookup. Tomorrow, when keys live in a B-tree or memtable, the same API still matters: you pay for path descent once, then decide insert vs reject at the leaf. Designing `SetEx` now avoids painting the SQL layer into a Get+Set corner.

---

## 1. The `UpdateMode` Enum

```go
type UpdateMode int

const (
    ModeUpsert UpdateMode = 0 // old Set() behavior
    ModeInsert UpdateMode = 1 // only if absent
    ModeUpdate UpdateMode = 2 // only if present
)
```

```go
func (kv *KV) SetEx(key, val []byte, mode UpdateMode) (updated bool, err error)
func (kv *KV) Set(key, val []byte) (bool, error) {
    return kv.SetEx(key, val, ModeUpsert)
}
```

`updated=false` means “mode refused the write” or “value unchanged” — not necessarily an I/O error.

---

## 2. Truth Table (Skeleton Decision Grid)

| Mode | Key exists? | Same value? | Action | `updated` |
| :--- | :---: | :---: | :--- | :---: |
| `ModeInsert` | no | — | insert | `true` |
| `ModeInsert` | yes | — | no-op | `false` |
| `ModeUpdate` | no | — | no-op | `false` |
| `ModeUpdate` | yes | no | overwrite | `true` |
| `ModeUpdate` | yes | yes | no-op | `false` |
| `ModeUpsert` | no | — | insert | `true` |
| `ModeUpsert` | yes | no | overwrite | `true` |
| `ModeUpsert` | yes | yes | no-op | `false` |

---

## 3. Mock Walkthrough

```text
Start: mem = {}

① SetEx("k1","v1", ModeUpdate)  → false   (nothing to update)
   mem = {}

② SetEx("k1","v1", ModeInsert)  → true
   mem = {k1:v1}

③ SetEx("k1","v1", ModeInsert)  → false   (PK collision)
   mem = {k1:v1}

④ SetEx("k1","yy", ModeUpdate)  → true
   mem = {k1:yy}

⑤ SetEx("k2","tt", ModeUpsert)  → true
   mem = {k1:yy, k2:tt}
```

ASCII timeline:

```text
mem:  {} ──Update✗──► {} ──Insert✓──► {k1:v1} ──Insert✗──► {k1:v1}
                                              ──Update✓──► {k1:yy}
                                              ──Upsert✓──► {k1:yy,k2:tt}
```

---

## 4. Why Push Modes into KV?

| Approach | Lookups | Race window |
| :--- | :---: | :--- |
| SQL `Get` then `Set` | 2 | Yes — another writer can slip between |
| KV `SetEx` with mode | 1 | Decision at the moment of write |

```text
Bad (two trips):
  Get(k) ──────────► exists?
  Set(k,v) ────────► write
       ▲
       └── another thread may Insert here

Good (one trip):
  SetEx(k,v, ModeInsert) ──► check+write atomically at leaf/map entry
```

Even with today’s in-memory map, the API is ready for B-Trees/LSM leaves later.

---

## 5. Mapping to Future SQL

| SQL statement | Mode |
| :--- | :--- |
| `INSERT INTO …` | `ModeInsert` |
| `UPDATE … SET …` | `ModeUpdate` |
| Upsert / replace | `ModeUpsert` |

Chapter **0204** wraps these into `DB.Insert` / `Update` / `Upsert`.

---

## Crucial Takeaways

* Blind `Set` cannot express SQL insert/update rules.
* `UpdateMode` + `SetEx` make writes conditional in one pass.
* `updated` reports mutation, not merely “no error.”
* Next: a relational `DB` façade over schema-aware rows (0204).
