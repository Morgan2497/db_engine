# Chapter 0202: Table Schema & Row Serialization

## Overview: Mapping Tables onto a Dumb KV Store
0201 gave us typed `Cell`s. A table needs many cells per row, a primary key, and isolation from other tables — still sitting on a KV store that only sees bytes.

This chapter defines:

* **`Schema`** — table blueprint (column names, types, which columns are PK)
* **`Row`** — `[]Cell` matching that blueprint
* **`EncodeKey` / `EncodeVal`** — split a row into KV key vs KV value

```text
Logical row                         Physical KV
───────────                         ───────────
┌──────┬─────┬─────┐                key = table\0 + PK cells
│ time │ src │ dst │   ──encode──►  val = non-PK cells
└──────┴─────┴─────┘
   ▲      ▲     ▲
   │      └──┴── primary key (example)
   └── non-key column
```

---

## The Concept & Theory: Tables Are a Mapping Problem

### The Fundamental Impedance Mismatch

A relational **row** is a tuple of named, typed fields. A KV store is a flat dictionary of byte strings. Bridging them is the central trick of embedded SQL engines (SQLite on its B-tree, Cockroach/TiDB on KV, etc.):

> **Choose which parts of the row become the KV key, and which become the KV value.**

### Primary Key as Physical Address

In our design (and in many clustered-index designs), the **primary key is not just a constraint** — it is the lookup address:
* Point queries (`WHERE pk = ?`) become a single `Get(EncodeKey(...))`.
* Uniqueness is “there can be only one value per key.”
* Non-key columns ride along in the value blob.

Secondary indexes (later) are extra KV mappings from secondary key → primary key (or covering columns). Understanding “PK = key” makes those future structures obvious.

### Why Split Key vs Value?

| Put in **key** | Put in **value** |
| :--- | :--- |
| Columns you look up by | Bulky attributes you fetch after lookup |
| Must be unique (PK) | May change without renaming the row’s address |
| Participate in sort/order (later) | Usually ignored by the index order |

If you stuffed the entire row into the key, every update would delete+reinsert a longer key (expensive). If you put the PK only in the value, you could not find the row without scanning.

### Table Namespaces and Prefix Isolation

One physical KV often holds **many logical tables**. Prefixing keys with `tableName + 0x00` creates a namespace:
* `user\0…` never collides with `users\0…` because of the null separator.
* Future range scans can iterate “all keys for table T” by seeking to `T\0` and stopping at the next prefix.

The null byte is a classic trick: it cannot appear in some string encodings, or is escaped; here table names are plain ASCII identifiers, so `\0` is a safe terminator.

### Schema as Contract

The `Schema` is the **catalog entry** for one table: names, types, PK indices. Encoding/decoding without a schema is impossible in our type-tag-free cell format. That is intentional: the relational layer owns meaning; the KV owns bytes.

---

## 1. Core Types

```go
type Column struct {
    Name string
    Type CellType
}

type Schema struct {
    Table string
    Cols  []Column
    PKey  []int    // column indices that form the primary key
}

type Row []Cell
```

### Skeleton schema: `link(time, src, dst)` PK `(src, dst)`

```text
Schema{
  Table: "link",
  Cols:  [0:time(i64), 1:src(str), 2:dst(str)],
  PKey:  [1, 2],
}

Column index:   0        1       2
              ┌──────┬───────┬───────┐
              │ time │  src  │  dst  │
              │ i64  │  str  │  str  │
              └──────┴───────┴───────┘
                         ▲       ▲
                         └── PK ─┘
```

---

## 2. The Golden Split Rule

| Goes into KV **key** | Goes into KV **value** |
| :--- | :--- |
| Table name + `0x00` separator | — |
| All **primary-key** cells (encoded) | All **non-PK** cells (encoded) |

```text
EncodeKey:  [ table bytes ][ 0x00 ][ PK0 ][ PK1 ]...
EncodeVal:  [ nonPK0 ][ nonPK1 ]...
```

Why? Point lookups and uniqueness live in the key. Payload columns ride in the value. Later indexes/LSM trees sort by key bytes.

---

## 3. Why the `0x00` After the Table Name?

Without a separator, table prefixes collide:

```text
Table "ab"  + key...  vs  Table "abc" + key...

Bad (no separator):
  ab  ...
  abc ...     ← "ab" is a prefix of "abc"; ambiguous boundary

Good:
  ab  \0 ...
  abc \0 ...  ← null byte ends the table namespace cleanly
```

```text
┌──────┬────┬────────────┐
│ link │\0  │ PK encodings│
└──────┴────┴────────────┘
```

---

## 4. Full Byte Trace: Row `(time=123, src="a", dst="b")`

### Logical row

```text
┌────────┬─────┬─────┐
│ 123    │ "a" │ "b" │
│ time   │ src │ dst │
│ NON-PK │ PK  │ PK  │
└────────┴─────┴─────┘
```

### KV key

```text
'l' 'i' 'n' 'k'  0x00  |  01 00 00 00  'a'  |  01 00 00 00  'b'
└─── table "link" ───┘  sep  └─ src len=1 ─┘  └─ dst len=1 ─┘
```

### KV value

```text
7B 00 00 00 00 00 00 00
└────── int64 123 (LE) ──────┘
```

### Side-by-side table

| Piece | Bytes |
| :--- | :--- |
| Table prefix | `l i n k 00` |
| PK `src="a"` | `01 00 00 00 a` |
| PK `dst="b"` | `01 00 00 00 b` |
| Val `time=123` | `7B 00 00 00 00 00 00 00` |

---

## 5. Decode Paths

| Method | Input | Fills |
| :--- | :--- | :--- |
| `DecodeKey` | KV key bytes | PK cells (after stripping `table\0`) |
| `DecodeVal` | KV value bytes | Non-PK cells |

```text
NewRow() → blank cells with Type=0
        → set Type from schema before each Decode
        → walk only PK indices (key) or only non-PK (val)
```

---

## 6. Multi-Table Namespace Mock

```text
Same physical KV map:

  "link\0..."   → rows of table link
  "user\0..."   → rows of table user
  "@schema_..." → later: schema metadata (0305)

No collisions: each table owns a prefix ended by 0x00.
```

---

## Crucial Takeaways

* Schema is the blueprint; Row is one typed tuple.
* **PK → key**, **rest → value**, prefixed by `table\0`.
* Null separator prevents table-name prefix collisions.
* Next: SQL `INSERT`/`UPDATE` need conditional writes → **Update Modes (0203)**.
