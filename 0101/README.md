# Chapter 0101: In-Memory Key-Value Store

## Overview: The Basement of the Database
Before SQL, schemas, logs, or disks, every database engine needs a **storage kernel**: a place that can store a key and return its value. This chapter builds the smallest possible version of that kernel — an in-memory map of raw bytes.

```text
┌─────────────────────────────────────────────────────────┐
│  Future chapters (SQL, rows, indexes, LSM…)             │
│         ▲                                               │
│         │  talk to this API                             │
│  ┌──────┴──────────────────────────────────────────┐    │
│  │  KV  Get / Set / Del                            │    │
│  │  mem: map[string][]byte                         │    │
│  └─────────────────────────────────────────────────┘    │
│         ▲                                               │
│         │  lives only in RAM (volatile)                 │
└─────────────────────────────────────────────────────────┘
```

**The Rule of this chapter:** Everything is raw `[]byte`. No types. No disk. No durability. If the process exits, the data is gone.

---

## The Concept & Theory: Why Start with a KV Store?

A production database (Postgres, MySQL, SQLite, RocksDB) looks enormous from the outside: SQL parsers, optimizers, transactions, indexes, replication. Underneath almost all of them is a simpler idea:

> **Associate an opaque key with an opaque value, and retrieve that value later.**

That idea is the **Key-Value (KV) abstraction**. Everything fancy in later chapters is either:
1. a way to *encode* richer data (rows, integers, schemas) into those opaque bytes, or
2. a way to make that association *survive crashes*, *sort correctly*, or *scale on disk*.

### Why not start with SQL tables?

If we began with `CREATE TABLE` and `SELECT`, we would mix three hard problems at once:
* **Meaning** — what is an integer vs a string?
* **Language** — how do we parse SQL text?
* **Storage** — where do bytes live, and how do we find them?

By isolating a dumb KV first, we get a stable **storage contract**. Higher layers can change (new SQL features, new indexes) without rewriting the basement. This is the same layering philosophy used by LSM engines (LevelDB/RocksDB) and by SQLite’s pager/B-tree split: keep the physical store simple and push intelligence upward.

### Volatility vs Durability (The Mental Model)

| Medium | Speed | Survives power loss? | Role in 0101 |
| :--- | :--- | :---: | :--- |
| CPU registers / L1 cache | Fastest | No | Not our concern yet |
| **RAM (`kv.mem`)** | Very fast | **No** | Entire database lives here |
| SSD / HDD | Slow | Yes | Deferred to 0103+ |

RAM is **volatile**: it requires continuous power. When the process exits or the machine reboots, every map entry disappears. That sounds like a defect, but it is a deliberate teaching step. You cannot appreciate logs, `fsync`, and checksums until you feel what “data only in RAM” really means.

### Hash Map as the First Index

We use Go’s `map` because it gives average **O(1)** Get/Set/Del. Conceptually it is already a tiny “index”:
* You never scan every key to find one key.
* You trade memory and hashing for speed.

Real databases later replace (or supplement) hash maps with **ordered** structures (B-Trees, LSM SSTables) so they can answer *range* questions (`WHERE id > 10`). A hash map cannot do ranges efficiently — keys are unordered. Remember that limitation; chapters 040x exist largely to fix it.

### Idempotency and “Did State Change?”

`Set` returning `updated=false` when the value is identical is not a pedantic detail. In databases, many layers care whether a write was a **no-op**:
* Avoid appending redundant log records (waste space, slow recovery).
* Replication can skip no-ops.
* SQL `UPDATE` row counts often reflect *actually changed* rows.

So from day one, mutations report **whether the logical state changed**, not merely “the call returned without error.”

---

## 1. The Data Structure

```go
type KV struct {
    mem map[string][]byte
}
```

| Piece | Why it exists |
| :--- | :--- |
| `map[...]` | O(1) average lookup by key |
| key as `string` | Go maps cannot use `[]byte` as a key (not comparable) |
| value as `[]byte` | Values can be any binary payload |
| Public API uses `[]byte` keys | Callers stay in “raw binary” land; the engine converts internally |

### Skeleton mock: empty database after `Open()`

```text
kv.mem
┌──────────────┐
│  (empty map) │
└──────────────┘
```

---

## 2. Why the Public API Uses `[]byte` (Not `string`)

A database is not a string dictionary. Later chapters will store encoded integers, row layouts, and checksums as binary. If the API forced `string`, every caller would have to convert payloads back and forth.

So the contract is:

> “Give me raw bytes. I will handle how they are stored.”

Internally:

```text
API key []byte("alice")  ──►  string("alice")  ──►  map key
API val []byte("42")     ──►  stored as []byte as-is
```

---

## 3. Operations (CRUD for Bytes)

| Method | Meaning | Returns |
| :--- | :--- | :--- |
| `Open()` | Allocate empty `mem` | `error` |
| `Close()` | Cleanup (no-op here) | `error` |
| `Get(key)` | Lookup value | `(val, ok, err)` |
| `Set(key, val)` | Insert or overwrite | `(updated, err)` |
| `Del(key)` | Remove key | `(deleted, err)` |

### The `updated` / `deleted` booleans

These are not errors. They answer: **“Did the database state actually change?”**

| Call | Situation | Result |
| :--- | :--- | :--- |
| `Set(k, v)` | key is new | `updated=true` |
| `Set(k, v)` | key exists, same value | `updated=false` (idempotent) |
| `Set(k, v2)` | key exists, different value | `updated=true` |
| `Del(k)` | key existed | `deleted=true` |
| `Del(k)` | key missing | `deleted=false` |

---

## 4. Step-by-Step Mock Trace

Start empty, then run the same lifecycle as the tests:

### Step A — `Set("morgankim", "developer")`

```text
BEFORE                          AFTER
┌──────────────┐                ┌─────────────────────────────┐
│  (empty)     │                │ "morgankim" → "developer"   │
└──────────────┘                └─────────────────────────────┘
                                updated = true
```

### Step B — `Set("morgankim", "developer")` again

```text
Value bytes identical → no write needed
updated = false
```

### Step C — `Get("morgankim")`

```text
┌─────────────────────────────┐
│ "morgankim" → "developer"   │ ──► ok=true, val="developer"
└─────────────────────────────┘
```

### Step D — `Del("morgankim")`

```text
BEFORE                          AFTER
┌─────────────────────────────┐ ┌──────────────┐
│ "morgankim" → "developer"   │ │  (empty)     │
└─────────────────────────────┘ └──────────────┘
                                deleted = true
```

### Step E — `Get("morgankim")` after delete

```text
ok = false   (key gone)
```

---

## 5. Visualizing Multiple Keys

```text
After:
  Set("user:1", "Morgan")
  Set("user:2", "Alice")
  Set("cfg",    "debug=1")

kv.mem (logical view — maps are unordered)
┌────────────┬──────────────┐
│ key        │ value        │
├────────────┼──────────────┤
│ "user:1"   │ "Morgan"     │
│ "user:2"   │ "Alice"      │
│ "cfg"      │ "debug=1"    │
└────────────┴──────────────┘
```

`Get("user:1")` returns `"Morgan"`.  
`Del("cfg")` removes only that row.

---

## 6. What This Chapter Deliberately Does Not Do

| Missing feature | Why it matters later |
| :--- | :--- |
| Disk persistence | Process exit = data loss → Chapter **0102/0103** |
| Serialization format | Need a byte layout for disk → **0102** |
| Crash recovery | Need a log + replay → **0103** |
| Durability (`fsync`) | Page cache can lie → **0104** |
| Torn-write detection | Partial records corrupt recovery → **0105** |
| Types / SQL | Relational layer starts at **0201** |

---

## Crucial Takeaways

* The KV API is the permanent **storage contract** for the rest of the engine.
* Keys and values are opaque bytes; higher layers decide meaning.
* `updated`/`deleted` report *state change*, not success/failure.
* RAM is fast and volatile — this chapter is the skeleton everything else hangs on.
