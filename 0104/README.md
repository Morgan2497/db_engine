# Chapter 0104: Fsync & Data Durability

## Overview: “Written” Is Not the Same as “On Disk”
Chapter 0103 appends encoded entries to a log file. That feels durable — until you learn what `Write()` actually does.

On modern OSes, a successful `Write()` usually means: **bytes landed in the Page Cache (RAM)**. The physical SSD/HDD may still be empty. Pull the power plug → those bytes evaporate.

Each file write does not directly map to a disk write. The OS has a memory cache. Writes
go to the cache first, then are synced to disk later. This allows merging repeated writes and
improves throughput. Repeated reads also benefit.
This cache is called the page cache. The page here matches the CPU virtual memory page.
A page is the smallest unit of IO, with a fixed size (usually 4K or 16K). You may ask how
disk IO cache relates to virtual memory. Look into mmap. Also note that database docs often
call B-tree nodes “pages”. Do not confuse them.
Besides the page cache, disks may also have their own RAM cache. To ensure data reaches
disk, an operation must flush all cache layers and wait for completion. This is the fsync
syscall on Linux.

In Go, call it via Sync() on *os.File.
```text
What you think happens:
  app.Write() ──────────────────────────────►  physical disk

What actually happens:
  app.Write() ──► OS Page Cache (RAM) ──?──►  physical disk
                       ▲
                       │  power loss here = data gone
```

This chapter implements **Durability** (the “D” in ACID) with `fsync`: force the OS to flush file data (and, on Unix, directory metadata) to hardware before we claim success.

---

## The Concept & Theory: Durability and the OS Lie

### ACID’s “D” in Plain Language

**Durability** means: once the database acknowledges a successful write, that write must remain observable after a crash, power loss, or reboot. Users build banking apps and shopping carts on that promise. If `Set` returns success but a brownout erases the bytes, the contract is broken — even if your Go code looked correct.

### Why Operating Systems Delay Disk Writes

Physical I/O is orders of magnitude slower than RAM. To keep the whole machine fast, kernels implement a **page cache**:
* `write()` copies into RAM pages marked **dirty**.
* The kernel later flushes dirty pages in large, efficient batches.
* From the app’s perspective, `write()` already “succeeded.”

That design is excellent for text editors and compilers. It is dangerous for databases, because “success” was measured against **volatile** memory, not **persistent** media.

There may be *another* cache inside the disk controller (write-back caching). True durability sometimes also needs drive-level flush commands; `fsync` is the portable application-level barrier that asks the OS to push data down as far as the platform guarantees.

### `fsync` as a Happens-Before Barrier

Think of `fsync` / `File.Sync()` as a synchronization fence:

```text
bytes may be only in RAM  ──Sync()──►  bytes must be on stable storage
         (before)                         (after returns)
```

Until `Sync` returns, you must not tell the user “committed.” After it returns, a process crash should not lose that record (hardware failure aside).

### Group Commit (Why Naive Per-Write Sync Is Expensive)

Calling `fsync` on every tiny record is correct but slow (thousands of syncs/sec vs millions of writes/sec into the page cache). Production engines often use **group commit**: buffer many transactions, then one `fsync` covers the batch. Our teaching engine syncs per write for clarity — the *principle* is the same: durability has a latency cost.

### Files vs Directories: Two Persistence Domains

On Unix, creating a file updates:
1. **inode / data blocks** — the file’s contents
2. **directory entries** — the name → inode mapping

You can fsync (1) and still lose (2) on crash → an **orphan inode**: data exists but no path reaches it. That is why `createFileSync` also syncs the parent directory. Databases that rename `wal.tmp` → `wal` face the same rule: rename is a directory operation and needs directory durability.

### Durability Without Atomicity Is Incomplete

Even after this chapter, a crash *during* the synced write can leave a **partial last record**. Durability says “what was fully synced stays.” It does not say “the last attempt was all-or-nothing.” That gap is **Atomicity**, fixed next with checksums.

---

## 1. The Page Cache Illusion

| Operation | Where data lives after success |
| :--- | :--- |
| `fp.Write(bytes)` | Often only in **volatile RAM** (dirty pages) |
| `fp.Sync()` / `fsync` | OS + drive confirm data on **non-volatile media** |

**Benefit of the cache:** merge many tiny writes into fewer large disk I/Os → huge throughput.  
**Danger for databases:** “Success” without fsync is a lie under power loss.

```text
┌──────────────┐     Write()      ┌──────────────┐     Sync()     ┌────────────┐
│  Application │ ───────────────► │  Page Cache  │ ─────────────► │   Disk     │
│  Log.Write   │                  │  (volatile)  │                │ (durable)  │
└──────────────┘                  └──────────────┘                └────────────┘
```

---

## 2. The Durability Contract

> Once the database returns success for a write, that data must survive a pulled power cord.

Standard `Write()` cannot fulfill this. We must issue:

> “Flush this file out of RAM caches, force the disk to write it, and **do not return** until hardware confirms.”

In Go: `os.File.Sync()` → Unix `fsync`.

---

## 3. The Hidden Trap: Directory Metadata

A Unix file is two things:

```text
┌─────────────────────────────────────────────┐
│  Parent directory                           │
│  ┌───────────────────────────────────────┐  │
│  │  name "db.log"  ──pointer──► inode     │  │  ← METADATA
│  └───────────────────────────────────────┘  │
└─────────────────────────────────────────────┘
                      │
                      ▼
              ┌──────────────┐
              │  file bytes  │  ← DATA
              └──────────────┘
```

| You fsynced… | Crash before directory sync | Result |
| :--- | :--- | :--- |
| File **data** only | Directory entry never hit disk | **Orphan file** — data on disk, OS can’t find the name |
| File data **and** parent directory | — | File exists and is findable after reboot |

**Rule:** Creating, renaming, or deleting a file on Unix requires `fsync` on the **parent directory** too. (Windows typically handles this for you.)

---

## 4. Skeleton Implementation

### Sync the parent directory

```go
func syncDir(file string) error {
    flags := os.O_RDONLY | syscall.O_DIRECTORY
    dirfd, err := syscall.Open(path.Dir(file), flags, 0o644)
    if err != nil {
        return err
    }
    defer syscall.Close(dirfd)
    return syscall.Fsync(dirfd)
}
```

```text
file path:  /var/data/engine.log
                │
                └── path.Dir → /var/data
                                 │
                                 └── open as directory fd → Fsync(fd)
```

### Safe create + open

```go
func createFileSync(file string) (*os.File, error) {
    fp, err := os.OpenFile(file, os.O_RDWR|os.O_CREATE, 0o644)
    if err != nil {
        return nil, err
    }
    if err = syncDir(file); err != nil {
        _ = fp.Close()
        return nil, err
    }
    return fp, nil
}
```

### Durable log write

```text
Log.Write(entry):
  1. Encode entry → []byte
  2. fp.Write(bytes)     // into page cache
  3. fp.Sync()           // force to disk  ← NEW in 0104
```

Wire format is still the **9-byte header** from 0103 — this chapter changes **I/O policy**, not layout.

---

## 5. Before / After Visualization

### 0103 (unsafe under power loss)

```text
Set("user","Morgan")
  └─ Write(encoded) → page cache ✓
  └─ return success
        │
        └─ ⚡ power loss → entry may never reach disk
```

### 0104 (durable)

```text
Set("user","Morgan")
  └─ Write(encoded) → page cache
  └─ Sync()         → physical disk ✓
  └─ return success
        │
        └─ ⚡ power loss → entry still on disk after reboot + replay
```

### Recovery mock

```text
1. Set user1 → "Morgan"
2. Set user1 → "Morgan Kim"     (override)
3. Del user2                    (tombstone if it existed)
4. Close / crash / reopen

After replay:
  Get(user1) → "Morgan Kim"
  Get(user2) → miss
```

---

## 6. Platform Split

| File | Behavior |
| :--- | :--- |
| `os_unix.go` | `createFileSync` + `syncDir` with real `syscall.Fsync` |
| `os_other.go` | Non-Unix stub: plain `OpenFile`, no directory sync |

Build tags keep Unix durability without breaking Windows builds.

---

## ⚠️ Looking Ahead: Torn Writes

`fsync` guarantees that **whatever made it to disk stays there**. It does **not** guarantee the last write was complete. Power mid-sector can leave a **half-written** final record. Chapter **0105** detects that with CRC32 checksums (Atomicity).

---

## Crucial Takeaways

* `Write()` ≠ durable; `Sync()` / `fsync` is the durability barrier.
* Unix needs directory fsync on create/rename/delete to avoid orphans.
* Entry layout unchanged from 0103; the win is I/O policy.
* Durability without atomicity still risks a corrupt last record → next chapter.
