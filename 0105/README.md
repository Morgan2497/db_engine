# Chapter 0105: Atomicity and Checksums

## Overview: The Last Record Can Still Lie
Chapter 0104 forced log bytes through `fsync` onto physical media. Durability (“D”) is covered. A new failure mode remains: **torn writes**.

If power dies *while* the disk is writing a multi-byte record, that **last** entry may be incomplete, zero-filled, or truncated. Blindly replaying it can poison `kv.mem` with garbage sizes and corrupt values.

```text
Valid history                          Torn tip
┌────────┬────────┬────────┐          ┌─────┐
│ rec 1  │ rec 2  │ rec 3  │   +      │ ??? │  ← half-written
└────────┴────────┴────────┘          └─────┘
     ▲                                    ▲
     └── safe after fsync                 └── must DETECT and IGNORE
```

This chapter adds **Atomicity** (“A”) at the record level: only **complete, checksum-verified** entries are replayed.

---

## The Concept & Theory: Atomicity at the Storage Layer

### What “Atomic” Means Here

In concurrency, “atomic” often means “appears indivisible to other threads.” In crash recovery, **atomic write** means:

> Either the whole record is present and trusted after reboot, or it is as if the write never happened.

Users should never observe a half-applied log entry that corrupts sizes and poisons the in-memory map. That is storage-level atomicity for our append-only design.

### Why Disks Make This Hard

Storage hardware commits data in **sectors** (512 B or 4 KiB). A multi-kilobyte log record spans many sectors. Power loss can commit some sectors and not others. The file size might even include the “failed” space filled with zeros or stale garbage. Your decoder, if naïve, will trust a length field inside that garbage and jump off a cliff.

### Detection vs Prevention

Two philosophical approaches:

| Approach | Idea | Tradeoff |
| :--- | :--- | :--- |
| **Prevention** | Double-fsync with a “commit pointer” sector; only advance pointer after data is safe | Strong, but **two** expensive syncs per commit |
| **Detection** | Store a checksum over the record; on read, verify or discard | One sync; recovery must tolerate a bad tip |

We choose **detection with CRC32**. It matches how many practical log formats work: assume the prefix of the log is good; stop at the first bad record.

### Checksums as Integrity Evidence

A checksum is a compact fingerprint of a byte range. If any bit in the range flips (torn write, bit rot, buggy copy), the recomputed fingerprint almost certainly mismatches.

Why not SHA-256? Cryptographic hashes defend against *adversaries*. We defend against *accidents*. CRC32 is:
* tiny (4 bytes on the wire),
* extremely fast,
* good at catching burst errors and zero-filled torn sectors.

Why not a simple sum? Summing zeros yields zero — a torn page of `NUL` bytes can look “valid.” CRCs are designed so structured garbage rarely verifies.

### Chain Integrity: Why We Stop, Not Skip

Our records are **variable length**. The only way to find record N+1 is to trust record N’s size fields. If record N’s header is corrupt, you cannot safely find N+1. Therefore recovery policy is:

```text
read → verify → apply → repeat
              ↘ fail → END OF LOG (ignore the tip)
```

Skipping ahead would require a different format (fixed-size slots, secondary indexes of offsets, etc.). For a teaching WAL, “prefix is valid; tip may be junk” is the right mental model.

### ACID So Far (Storage Slice)

| Letter | Mechanism in our engine |
| :--- | :--- |
| **A**tomicity | CRC + ReadFull; bad tip discarded |
| **C**onsistency | Still mostly app/schema-level (later) |
| **I**solation | Not yet (single-threaded mental model) |
| **D**urability | fsync after append (0104) |

We are building the storage foundations real engines rely on before transactions get fancy.

---

## 1. Three Torn-Write Failure Modes

Assume we intended to append a 1000-byte record:

| Mode | File size | Payload on disk | Danger |
| :--- | :--- | :--- | :--- |
| Partial data | +1000 | Only some bytes correct | Wrong payload, maybe OK-looking sizes |
| Garbage / zeros | +1000 | Old garbage or `0x00` | Fake lengths → huge seeks |
| Truncated | +500 | Cut mid-record | Incomplete header/payload |

Previously fsynced records stay fine. Only the **active last write** is unsafe.

---

## 2. Hardware vs Software Atomicity

Disks write in **sectors** (512 B historically, often 4 KiB now). One sector write is roughly atomic in hardware. Multi-sector records are not.

| Strategy | Cost | Approach |
| :--- | :--- | :--- |
| Double-fsync pointer sector | Slow (2× fsync) | Write data, fsync, update “last good” pointer, fsync again |
| **Checksum per record** | Fast | Write record with CRC; on read, verify or stop |

We choose **CRC32** — detect corruption without a second fsync barrier per entry.

---

## 3. New Wire Format: 13-Byte Header

```text
┌────────┬──────────┬──────────┬─────────┬──────────┬──────────┐
│ crc32  │ key size │ val size │ deleted │ key data │ val data │
│ 4 B    │ 4 B      │ 4 B      │ 1 B     │   N B    │   M B    │
└────────┴──────────┴──────────┴─────────┴──────────┴──────────┘
Offset: 0        4          8         12        13
```

| Offset | Size | Field |
| :---: | :---: | :--- |
| 0 | 4 | `crc32` — CRC32-IEEE of bytes from offset 4 to end of record |
| 4 | 4 | `key_size` (uint32 LE) |
| 8 | 4 | `val_size` (uint32 LE; 0 for tombstones) |
| 12 | 1 | `deleted` |
| 13 | N | key |
| 13+N | M | value |

### Skeleton: `key="k1"`, `val="xxx"`, not deleted

```text
CRC covers: [key_size|val_size|deleted|key|val]
            everything AFTER the crc field itself

Layout:
[ C0 C1 C2 C3 | 2 0 0 0 | 3 0 0 0 | 0 | k 1 | x x x ]
  └─ crc32 ─┘   key=2     val=3     △
                                    deleted
```

---

## 4. Why CRC32 (Not SHA-256, Not a Simple Sum)?

| Hash | Why / why not |
| :--- | :--- |
| SHA-256 / MD5 | Cryptographic strength we don’t need; too slow for every write |
| Simple byte sum | **Null-byte poisoning:** a torn sector of zeros sums to `0` — indistinguishable from “valid empty” |
| **CRC32** | Fast, 4 bytes, designed to catch burst/bit errors; zeros don’t “look valid” by accident |

```text
Torn zeros:  00 00 00 00 00 ...
Simple sum:  0  → might look "OK"
CRC32:       non-trivial → ErrBadSum → stop replay
```

---

## 5. `io.ReadFull` and Unexpected EOF

`io.Reader.Read` may return fewer bytes than requested. For fixed headers that is fatal.

| Error from `ReadFull` | Meaning for us |
| :--- | :--- |
| `nil` | Got exact buffer length |
| `io.EOF` | Clean end of file (no more records) |
| `io.ErrUnexpectedEOF` | Hit EOF **mid-record** → torn/truncated write |
| `ErrBadSum` | Bytes present but CRC mismatch → corrupt tip |

Recovery rule in `Log.Read`:

```text
Decode returns EOF | UnexpectedEOF | ErrBadSum
        │
        ▼
  treat as end-of-log (eof=true)
  DO NOT crash; DO NOT skip ahead
```

**Why not skip ahead?** Size fields inside a torn header are untrustworthy. A garbage `val_size` might say “jump 2.5 GB.” Corruption breaks the chain — the log ends here.

---

## 6. Recovery Mock Visualization

```text
Disk after crash:

┌──────────────┬──────────────┬─────────────┐
│ good entry A │ good entry B │ GARBAGE tip │
│ CRC ✓        │ CRC ✓        │ CRC ✗ / short│
└──────────────┴──────────────┴─────────────┘

Replay:
  apply A → mem updated
  apply B → mem updated
  tip fails CRC / UnexpectedEOF → STOP
Final mem: state as of B  (A and B durable; tip discarded)
```

Test-shaped example:

```text
1. Write key1 → value1 (valid, fsynced)
2. Append raw garbage {0xDE,0xAD,0xBE,0xEF,0x00} without care
3. Reopen DB
4. Get("key1") still "value1"   ← garbage tip ignored
```

---

## 7. ACID Foundation So Far

```text
0103  Append-only log     → history & recovery path
0104  fsync               → Durability (D)
0105  CRC32 + ReadFull    → Atomicity (A) per record
```

```text
┌─────────────────────────────────────────────┐
│  Storage engine survival kit                │
│  • Never overwrite in place                 │
│  • Sync before claiming success             │
│  • Verify every record before trusting it   │
└─────────────────────────────────────────────┘
```

This is the same philosophical core behind embedded engines like SQLite’s careful write path: plain file I/O is not a database.

---

## Crucial Takeaways

* Torn last records are normal under power loss; detection is mandatory.
* CRC lives in a 4-byte prefix; it covers the rest of the record.
* `ReadFull` + `ErrUnexpectedEOF` catch truncated tips.
* On bad sum / unexpected EOF: **end replay**, keep prior good state.
* Next leap: leave raw bytes and build **typed relational cells** (0201).
