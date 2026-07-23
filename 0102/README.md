# Chapter 0102: Serialization (Encode / Decode)

## Overview: Flattening Living Structures into Bytes
In Chapter 0101, the KV store lived entirely in RAM. That is fast — and fatal. When the process exits, `kv.mem` vanishes.

To survive on disk (or travel over a network), a Go struct must become a **continuous stream of raw bytes**. That conversion is **serialization**. Reading those bytes back into a struct is **deserialization**.

```text
RAM world (structs)              Disk / wire world (bytes)
───────────────────              ─────────────────────────
Entry{key, val}   ── Encode() ─►  []byte{...}
Entry{key, val}   ◄─ Decode() ──  io.Reader stream
```

This chapter does **not** open files yet. It only defines the **wire format** that later chapters will append to a log.

---

## 1. The Wire Format (Skeleton Layout)

Every serialized entry is one contiguous blob:

```text
┌──────────┬──────────┬──────────┬──────────┐
│ key size │ val size │ key data │ val data │
│ 4 bytes  │ 4 bytes  │   N B    │   M B    │
└──────────┴──────────┴──────────┴──────────┘
     ▲          ▲
     │          └── length of val payload (uint32 little-endian)
     └───────────── length of key payload (uint32 little-endian)
```

| Offset | Size | Field | Meaning |
| :---: | :---: | :--- | :--- |
| 0 | 4 | `key_size` | `uint32` LE length of key |
| 4 | 4 | `val_size` | `uint32` LE length of value |
| 8 | N | `key_data` | raw key bytes |
| 8+N | M | `val_data` | raw value bytes |

**Total size** = `8 + len(key) + len(val)`.

Why length prefixes? Without them, a decoder cannot know where the key ends and the value begins when both are variable length.

---

## 2. Concrete Mock: `key="k1"`, `val="xxx"`

### Logical view

```text
Entry
┌──────┬───────┐
│ key  │ "k1"  │   len = 2
│ val  │ "xxx" │   len = 3
└──────┴───────┘
```

### Encoded bytes (little-endian)

```text
Offset:   0         4         8      10
        ┌─────────┬─────────┬──────┬─────────┐
        │ 2 0 0 0 │ 3 0 0 0 │ k  1 │ x  x  x │
        └─────────┴─────────┴──────┴─────────┘
          key_size  val_size   key     value
```

| Bytes | Decimal | Meaning |
| :--- | :--- | :--- |
| `2,0,0,0` | 2 | key length |
| `3,0,0,0` | 3 | value length |
| `'k','1'` | — | key payload |
| `'x','x','x'` | — | value payload |

Same format for `key="a"`, `val="bb"`:

```text
[1,0,0,0, 2,0,0,0, 'a', 'b','b']
 └─ size 1 ─┘ └─ size 2 ─┘  └key┘ └─ val ─┘
```

---

## 3. Encode — Building the Buffer

```go
func (ent *Entry) Encode() []byte {
    data := make([]byte, 4+4+len(ent.key)+len(ent.val))
    binary.LittleEndian.PutUint32(data[0:4], uint32(len(ent.key)))
    binary.LittleEndian.PutUint32(data[4:8], uint32(len(ent.val)))
    copy(data[8:], ent.key)
    copy(data[8+len(ent.key):], ent.val)
    return data
}
```

### Skeleton mock for `key="a"`, `val="bb"` (11 bytes)

```text
Step 1: allocate
data = [ _, _, _, _,  _, _, _, _,  _,  _, _ ]
         0  1  2  3   4  5  6  7   8   9 10

Step 2: write key_size=1 at [0:4]
data = [ 1, 0, 0, 0,  _, _, _, _,  _,  _, _ ]

Step 3: write val_size=2 at [4:8]
data = [ 1, 0, 0, 0,  2, 0, 0, 0,  _,  _, _ ]

Step 4: copy key at offset 8
data = [ 1, 0, 0, 0,  2, 0, 0, 0, 'a',  _, _ ]

Step 5: copy val at offset 9
data = [ 1, 0, 0, 0,  2, 0, 0, 0, 'a','b','b' ]
```

---

## 4. Decode — Reading from a Stream

Decode does **not** receive a finished `[]byte` slice of known length. It receives an `io.Reader` — a stream. You must:

1. Read the **8-byte header** (both length fields).
2. Allocate key/value buffers of those exact sizes.
3. Read the payloads.

```go
func (ent *Entry) Decode(r io.Reader) error {
    header := make([]byte, 8)           // buffer for BOTH length fields
    if _, err := io.ReadFull(r, header); err != nil {
        return err
    }
    keySize := binary.LittleEndian.Uint32(header[0:4])
    valSize := binary.LittleEndian.Uint32(header[4:8])

    ent.key = make([]byte, keySize)
    ent.val = make([]byte, valSize)

    if _, err := io.ReadFull(r, ent.key); err != nil { return err }
    if _, err := io.ReadFull(r, ent.val); err != nil { return err }
    return nil
}
```

### What is a “buffer” here?

A **buffer** is temporary memory (`[]byte`) that holds raw bytes while you process them. The stream does not hand you typed integers — only bytes. So you catch the next 8 bytes in `header`, then interpret them.

### Why `_` in `_, err := io.ReadFull(...)`?

`ReadFull` returns `(n int, err error)`. On success, `n` always equals `len(header)`. You only care about `err`, so `_` discards `n`.

---

## 5. Decode Trace: Stream Position Mock

Input stream for `key="a"`, `val="xxx"` (12 bytes):

```text
Stream:  [1,0,0,0 | 3,0,0,0 | a | x,x,x]
Cursor:   ^
```

| Step | Action | Buffer after | Cursor |
| :--- | :--- | :--- | :---: |
| 1 | `ReadFull` into `header` (8 bytes) | `header=[1,0,0,0,3,0,0,0]` | 8 |
| 2 | Decode sizes | `keySize=1`, `valSize=3` | 8 |
| 3 | `ReadFull` into `ent.key` | `key=['a']` | 9 |
| 4 | `ReadFull` into `ent.val` | `val=['x','x','x']` | 12 |

```text
BEFORE decode          AFTER decode
(raw stream)           Entry{key:"a", val:"xxx"}

[1 0 0 0 3 0 0 0 a x x x]
 └──── header ────┘ └k┘ └─v─┘
```

---

## 6. Why `io.ReadFull` (Not Plain `Read`)?

| Call | Guarantee |
| :--- | :--- |
| `Read(buf)` | May return **fewer** bytes than `len(buf)` |
| `ReadFull(r, buf)` | Fills **all** of `buf`, or returns an error |

For a fixed 8-byte header, a partial read would mis-parse sizes and corrupt the entry. `ReadFull` makes the contract exact.

---

## 7. Little-Endian Reminder

`uint32(2)` as little-endian:

```text
Memory:  [2, 0, 0, 0]     least-significant byte first
Not:     [0, 0, 0, 2]     (that would be big-endian)
```

x86/ARM CPUs are little-endian, so this matches native layout and is common for local disk formats. (Later chapters will use **big-endian** when sort order matters for indexes.)

---

## 8. How This Fits the Engine Roadmap

```text
0101  In-memory map          ← API exists
0102  Entry Encode/Decode    ← YOU ARE HERE (wire format)
0103  Append-only log file   ← write Encoded bytes to disk
0104  fsync                  ← make those bytes durable
0105  CRC32                  ← detect torn Encoded records
```

---

## Crucial Takeaways

* Serialization = struct → contiguous `[]byte`; deserialization = stream → struct.
* Length prefixes let variable-size key/value share one blob without separators.
* `header := make([]byte, 8)` is a **scratch buffer for both uint32 length fields**.
* `ReadFull` is required so headers and payloads are never half-read.
* This format is the foundation every later log/KV record builds on.
