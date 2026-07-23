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
