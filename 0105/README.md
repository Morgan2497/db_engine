# Chapter 0105: Atomicity and Checksums

## Overview: The Illusion of Safe Writes
In the previous chapter, we implemented `fsync` to bypass the OS Page Cache and force data directly to physical disk platters. While this guarantees durability (the "D" in ACID), it introduces a new vulnerability: **Incomplete or Torn Writes**. 

File writes are not inherently atomic. If power is lost mid-write, the disk does not guarantee the entire payload is saved. This chapter implements **Atomicity** (the "A" in ACID) at the storage layer using mathematical checksums to detect and discard corrupted state.

---

## The Core Problem: Torn Writes
When appending a large record (e.g., 1000 bytes) to our log file, a sudden power failure or kernel panic can result in three distinct failure states:

* **Partial Data:** The file size increases by the full 1000 bytes, but only a fraction of the data makes it to the physical sectors.
* **Garbage Data:** The file size increases by 1000 bytes, but no actual payload is written. The OS leaves previously deleted garbage bytes (or zeros) in that allocated space.
* **Truncated Write:** The file size only increases by a fraction (e.g., 500 bytes), cleanly cutting off the payload.

In all scenarios, the previously `fsynced` records remain pristine. Only the *last* record being actively written is corrupted. If our recovery loop blindly trusts the file size or the entry header, it will read this corrupted or garbage data into our RAM map, destroying the integrity of the database.

### Hardware-Level Atomicity vs. Software Atomicity
The CPU handles atomic memory operations for concurrency control. Disks, however, operate in **Sectors** (historically 512 Bytes, modern drives use 4 Kilobytes). Writing a single sector to a physical disk is generally considered atomic by the hardware controller. 

Many legacy software systems achieve atomicity by reserving the first sector of a file to store a pointer to the last known good entry. This requires writing the data, calling `fsync`, updating the pointer sector, and calling `fsync` again. We can achieve software-level atomicity without the heavy performance penalty of double-fsyncing by utilizing **Checksums**.

---

## The Implementation: CRC32 Verification
We are expanding our binary header from 9 bytes to 13 bytes to prepend a 4-byte checksum. By calculating a mathematical hash of our exact payload before writing it, we can verify that exact payload upon reading it.

### The New Binary Layout
The physical layout of our log entries on disk is now:

| Field | Size (Bytes) | Description |
| :--- | :--- | :--- |
| `crc32` | 4 | Mathematical hash of the Key + Value data |
| `key_size` | 4 | Unsigned integer representing key length |
| `val_size` | 4 | Unsigned integer representing value length |
| `deleted` | 1 | Boolean tombstone flag (1 for true, 0 for false) |
| `key_data` | Variable | The raw byte slice of the key |
| `val_data` | Variable | The raw byte slice of the value |

---

## Checksum Strategy: CRC32 vs. Cryptographic Hashes
Checksums are our defense against both torn writes and silent hardware corruption (e.g., bit-rot or flipped bits in physical memory/disk). However, choosing the *right* hash function is crucial for high-throughput database performance.

We explicitly avoid cryptographic hashes like SHA-256 or MD5. Cryptographic hashes are designed for security and collision resistance, making them computationally heavy and detrimental to write throughput. We only need to detect hardware failure or torn writes, making CRC32 (Cyclic Redundancy Check) vastly faster and smaller (4 bytes).

**The Null-Byte Poisoning Problem:**
Why use `crc32` instead of a simple 16-bit integer sum (like the one used in TCP/IP headers)? If the OS allocates space on the disk but fails to write the data, the disk sectors might be filled with zero-bytes (`\x00`). If you sum up a million zero-bytes, the result is still `0`. A simple sum cannot differentiate between "valid data that happens to sum to zero" and "empty garbage." CRC32 algorithms are designed to produce a non-zero hash even for null-byte payloads, easily catching empty-sector corruption.

---

## Execution and Error Handling 
During the `KV.Open()` crash recovery loop, we must meticulously handle how bytes are read from the disk into our engine. 

### The Trap of `io.Reader` and the Power of `io.ReadFull()`
When working with low-level file I/O, developers often mistakenly assume that asking the OS for a specific number of bytes will always return exactly that number of bytes—or an End-Of-File (EOF) error. This is false. 

The standard `io.Reader` interface is legally allowed to return fewer bytes than the requested buffer length. Kernel-level I/O scheduling, system interrupts, or pipe buffers can cause a read to return prematurely. Because our engine is built to operate across OS boundaries (developing on a Linux system but producing executables for Windows environments), we cannot rely on OS-specific file reading quirks. We must force the system to give us exactly what we asked for.

Instead of writing a manual `for` loop to keep reading until our buffer is full, we leverage Go's standard library: `io.ReadFull(r Reader, buf []byte)`.

`io.ReadFull()` guarantees the buffer is completely filled. If it hits an EOF *before* the buffer is full, it returns a highly specific error: `io.ErrUnexpectedEOF`. For our engine, this error is a perfect, deterministic flag that we just encountered a truncated, torn write.

---

## Handling Incomplete Log Records (The Recovery Loop)
During engine startup, our `KV.Open()` function replays the log from top to bottom. When `Entry.Decode()` returns either `io.ErrUnexpectedEOF` or our custom `ErrBadSum` (checksum mismatch), it flags `eof=true`.

**The critical rule of recovery:** We do not crash. We gracefully ignore the final, corrupted record and halt the read loop.

**Why only the *last* record?**
We cannot skip ahead because our engine's architecture relies on the binary header to tell us the size of the record (`KeySize` and `ValSize`). If a write was torn or corrupted by garbage data, the size headers themselves are completely untrustworthy. A corrupted 4-byte size header might incorrectly tell the engine to jump ahead 2.5 Gigabytes to find the next record. 

Therefore, any corruption is treated as a fatal break in the log's chain. Once the chain is broken, the log is effectively over.

---

## Summary: The Foundation of ACID
By combining **Append-Only Logs**, **OS-level `fsync`**, and **CRC32 Checksums**, we have built a storage engine that can survive sudden power loss and guarantee that previously successful writes are never lost or corrupted. 

This specific combination is the absolute core of robust, embedded data storage. It is the exact reason why engines like SQLite are the gold standard for local, non-cloud applications. Basic file operations simply cannot guarantee Durability (saving to physical metal) and Atomicity (all-or-nothing writes).

As we continue to build out this engine, remember that this log is only the persistence layer. When we eventually introduce complex in-memory data structures (like B-Trees for indexing), we will need to re-verify our Atomicity and Durability guarantees to ensure they hold up under concurrency.
