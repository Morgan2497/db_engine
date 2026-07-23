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
