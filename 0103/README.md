# 💾 Step 0103: Log Storage

> **Overview:** To survive power loss and crashes, a database must persist data to a non-volatile disk. Instead of modifying files in place (which is slow and complex), we use an **Append-Only Log**. Every update or deletion is recorded chronologically at the end of the file.

### Append-only Logs
Append-only logs are data structures that allow new records to be added exclusively to the end of a sequence, rendering all prior data immutable and prohibiting any modifications or deletions.  
This design treats storage as a linear, chronologically ordered log where entries are written sequentially, which optimizes disk I/O by leveraging contiguous allocation and eliminating 
the random write patterns typical of update-in-place storage

## 📖 Core Architectural Concepts

### 1. Incremental Updates and State Reconstruction
When the database starts, it reads the log from top to bottom and applies updates in order. For example, a log with 4 sequential operations:
1. `set k1=x` ➔ State: `{k1=x}`
2. `set k2=y` ➔ State: `{k1=x, k2=y}`
3. `set k1=z` ➔ State: `{k1=z, k2=y}`
4. `del k2`   ➔ Final State: `{k1=z}`

By replaying this history, the database reconstructs the final, correct state in memory (`{k1=z}`). Later entries strictly override earlier ones.

### 2. Beyond Durability: Other Uses for Logs
Because a log accurately serializes state changes (even with concurrent transactions on a single node), it is extremely versatile. Logs form the backbone for:
* **Replication:** Sending the stream of log entries to backup servers.
* **Rollbacks (Undo):** Reversing operations during transaction failures.

### 3. The Size Limitation
A log cannot grow forever. If it did, booting the database would take hours. Therefore, a log is strictly **auxiliary storage**. Once a log reaches a certain size, it is flushed and merged into a main, permanent data structure (like an LSM-Tree or B+Tree) which we will implement in later chapters.

---

## 🛠️ Data Structures & Serialization

Because we never erase data from the log, we handle deletions by appending a **tombstone** record. We add a `deleted` flag to our `Entry` struct to distinguish between updates and deletions.

```go
type Entry struct {
    key     []byte
    val     []byte
    deleted bool
}
```

### The 9-Byte Binary Layout
To serialize the new boolean flag, our binary wire format expands to include a 1-byte marker between the sizes and the payload data.

```text
| key size | val size | deleted | key data | val data |
| 4 bytes  | 4 bytes  | 1 byte  |   ...    |   ...    |
```

*You must modify `Encode()` and `Decode()` to support this exact byte layout.*

---

## 💾 File I/O Mechanics
We introduce a dedicated `Log` struct to manage direct interactions with the operating system's file descriptor.

```go
type Log struct {
    FileName string
    fp       *os.File
}
```

### Opening the Log File
We use the `os` package to open the file with Read/Write permissions, and automatically create it if it doesn't exist:

```go
func (log *Log) Open() (err error) {
    log.fp, err = os.OpenFile(log.FileName, os.O_RDWR|os.O_CREATE, 0o644)
    return err
}

func (log *Log) Close() error {
    return log.fp.Close()
}
```

### Reading and Writing
Writing simply encodes the struct and appends it to the file. Reading is slightly more complex: it must properly detect `io.EOF` (End Of File) so the database knows when the log has been fully replayed. `Entry.Decode()` must safely propagate errors returned by `io.Reader`.

```go
func (log *Log) Write(ent *Entry) error {
    _, err := log.fp.Write(ent.Encode())
    return err
}

func (log *Log) Read(ent *Entry) (eof bool, err error) {
    err = ent.Decode(log.fp)
    if err == io.EOF {
        return true, nil
    } else if err != nil {
        return false, err
    } else {
        return false, nil
    }
}
```

---

## 🧠 Database Engine Integration
The `KV` struct is updated to hold both the physical disk log and the fast in-memory map.

```go
type KV struct {
    log Log
    mem map[string][]byte
}
```

### Boot Cycle (KV.Open)
When the database boots, `KV.Open()` must:
1. Open the log file.
2. Initialize the `kv.mem` map.
3. Repeatedly call `Log.Read()` in a loop until it hits `io.EOF`.
4. Apply the records to `kv.mem`, ensuring later entries overwrite earlier ones and deleted keys are removed.

### Execution Cycle (Set and Del)
To guarantee data is not lost, all mutations must now write to the disk log first.

```go
func (kv *KV) Set(key []byte, val []byte) (updated bool, err error)
func (kv *KV) Del(key []byte) (deleted bool, err error)
```

Once the disk write (`kv.log.Write`) succeeds, the database updates the fast memory map.

---

## ⚠️ Looking Ahead: The Power Loss Problem
While the log persists incremental updates to disk, a critical vulnerability remains. What happens if the server loses power in the exact millisecond that `Log.Write()` is executing? A half-written (torn) record will corrupt the file. Ensuring Atomicity in the face of power loss is the next major challenge to solve.

