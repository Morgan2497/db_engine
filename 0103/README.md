# Chapter 0103: Append-Only Log Storage

## Overview: Surviving Beyond RAM
Chapters 0101–0102 gave us an in-memory map and a byte wire format. Still: when the process dies, data dies.

This chapter adds **persistence** with an **append-only log**. Instead of editing records in place (slow, complex, crash-prone), every change is written **at the end of a file**. On boot, we replay the log from top to bottom and rebuild `kv.mem`.

```text
┌──────────────────────────────────────────────────────────┐
│  Application                                             │
│     Set / Del                                            │
│        │                                                 │
│        ▼                                                 │
│  ┌─────────────┐     append      ┌────────────────────┐  │
│  │  kv.mem     │ ◄── replay ───  │  log file on disk  │  │
│  │  (fast RAM) │                 │  (durable history) │  │
│  └─────────────┘                 └────────────────────┘  │
└──────────────────────────────────────────────────────────┘
```

---

## 1. Why Append-Only?

| Approach | Problem |
| :--- | :--- |
| Update-in-place | Random disk I/O; crash mid-overwrite corrupts the record |
| Append-only | Sequential writes; old bytes stay immutable; recovery = replay |

Append-only logs are chronological. Prior data is never modified or deleted in place. New facts are always added at the end.

---

## 2. State Reconstruction (Skeleton Replay)

Log operations in order:

```text
Log file (chronological)
┌────┬─────────────────────────┐
│ #1 │ set k1 = x              │
│ #2 │ set k2 = y              │
│ #3 │ set k1 = z              │
│ #4 │ del k2                  │
└────┴─────────────────────────┘
```

Replay into RAM:

```text
After #1:  { k1:x }
After #2:  { k1:x, k2:y }
After #3:  { k1:z, k2:y }      ← later write overrides earlier
After #4:  { k1:z }            ← tombstone removes k2
```

**Rule:** Later entries strictly override earlier ones. The log is history; the map is the latest snapshot.

---

## 3. Tombstones: Deleting Without Erasing

Because we never erase bytes from the log, a delete is another append: a **tombstone**.

```go
type Entry struct {
    key     []byte
    val     []byte
    deleted bool   // true = tombstone
}
```

### Visual: set then delete

```text
Disk log growth →→→

┌──────────────────────┐  ┌──────────────────────┐
│ SET key=user1 val=M │  │ DEL key=user1        │
│ deleted=0            │  │ deleted=1  (no val)  │
└──────────────────────┘  └──────────────────────┘

Replay: first puts user1, then tombstone removes it.
Final mem: user1 absent.
```

---

## 4. The 9-Byte Binary Layout

0102 had an 8-byte header (two lengths). 0103 inserts a **1-byte deleted flag**:

```text
┌──────────┬──────────┬─────────┬──────────┬──────────┐
│ key size │ val size │ deleted │ key data │ val data │
│ 4 bytes  │ 4 bytes  │ 1 byte  │   ...    │   ...    │
└──────────┴──────────┴─────────┴──────────┴──────────┘
Offset: 0          4          8         9
```

| Offset | Size | Field |
| :---: | :---: | :--- |
| 0 | 4 | `key_size` (uint32 LE) |
| 4 | 4 | `val_size` (uint32 LE) |
| 8 | 1 | `deleted` (`0` = set, `1` = tombstone) |
| 9 | N | key bytes |
| 9+N | M | value bytes (often empty for deletes) |

### Mock: `Set("k1","xxx")`

```text
[2,0,0,0 | 3,0,0,0 | 0 | k 1 | x x x ]
  key=2     val=3    △        key   val
                     deleted=0
```

### Mock: `Del("k1")`

```text
[2,0,0,0 | 0,0,0,0 | 1 | k 1 ]
  key=2     val=0    △   key
                     deleted=1
```

---

## 5. The `Log` Struct (File Wrapper)

```go
type Log struct {
    FileName string
    fp       *os.File
}
```

| Method | Role |
| :--- | :--- |
| `Open()` | `os.OpenFile(..., O_RDWR\|O_CREATE, 0644)` |
| `Close()` | close file descriptor |
| `Write(ent)` | `fp.Write(ent.Encode())` — append encoded bytes |
| `Read(ent)` | `ent.Decode(fp)`; map `io.EOF` → `(eof=true, nil)` |

```text
Write path:
  Entry ──Encode()──► []byte ──Write()──► end of file

Read path:
  file bytes ──Decode()──► Entry ──► applied to mem
```

---

## 6. KV Integration: Disk First, Then RAM

```go
type KV struct {
    log Log
    mem map[string][]byte
}
```

### Boot cycle (`Open`)

```text
1. Open log file
2. mem = empty map
3. loop:
     Read next Entry
     if EOF → stop
     if deleted → delete(mem, key)
     else       → mem[key] = val
4. Ready to serve Gets from RAM
```

### Mutation cycle (`Set` / `Del`)

```text
Set(key, val):
  ┌─────────────────────┐
  │ 1. log.Write(entry) │  ← MUST succeed first
  │ 2. mem[key] = val   │  ← then update RAM
  └─────────────────────┘

Del(key):
  ┌──────────────────────────┐
  │ 1. log.Write(tombstone)  │
  │ 2. delete(mem, key)      │
  └──────────────────────────┘
```

If we updated RAM first and crashed before the disk write, reboot would lose the change. **Log wins.**

---

## 7. Full Lifecycle Mock

```text
Action                  Log file (append)              mem
──────                  ─────────────────              ───
Open (empty)            (empty)                        {}
Set("a","1")            [SET a=1]                      {a:1}
Set("b","2")            [SET a=1][SET b=2]             {a:1,b:2}
Set("a","9")            ...[SET a=9]                   {a:9,b:2}
Del("b")                ...[DEL b]                     {a:9}
Close / crash
Open (replay)           read all entries again         {a:9}
```

---

## 8. Why Logs Cannot Grow Forever

Replaying a multi-gigabyte log on every boot would take forever. A log is **auxiliary** storage. Later chapters flush / compact history into primary structures (B-Trees, LSM SSTables). Until then: log = truth; mem = cache of latest values.

Logs also enable:

* **Replication** — ship the byte stream to replicas
* **Undo / rollback** — reverse entries on failure (future)

---

## ⚠️ Looking Ahead: The Power-Loss Hole

`Write()` appends bytes, but the OS may hold them in the **page cache**. A power loss can drop “successful” writes. Chapter **0104** adds `fsync`. Even with fsync, a mid-write crash can leave a **torn** last record — Chapter **0105** detects that with checksums.

---

## Crucial Takeaways

* Persistence = append encoded `Entry`s; recovery = replay in order.
* Deletes are tombstones, not in-place erasures.
* Always write the log **before** updating `mem`.
* The 9-byte header extends 0102 with a `deleted` flag.
