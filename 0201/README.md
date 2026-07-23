# Chapter 0201: Relational Data Types (`Cell`)

## Overview: Leaving the Land of Opaque Bytes
Chapters 0101–0105 built a durable KV basement that only understands `[]byte`. Relational databases need **typed columns**: integers, strings, later floats, etc.

This chapter introduces the first relational primitive: the **`Cell`** — one typed value that can encode/decode itself into bytes without allocating a fresh buffer on every encode.

```text
Storage layer (010x)              Relational layer (020x+)
────────────────────              ────────────────────────
[]byte key / []byte val     ◄──   Cell / Row / Schema / SQL
     dumb & fast                   typed & meaningful
```

---

## 1. The `Cell` Union

```go
type CellType uint8
const (
    TypeI64 CellType = 1
    TypeStr CellType = 2
)

type Cell struct {
    Type CellType
    I64  int64
    Str  []byte
}
```

| Field | When used |
| :--- | :--- |
| `Type` | Discriminator: which payload is live |
| `I64` | Live when `Type == TypeI64` |
| `Str` | Live when `Type == TypeStr` |

Go has no native tagged unions, so unused fields sit idle. That is intentional: simple, fast, no `any` boxing.

### Skeleton mock

```text
Cell{Type: TypeI64, I64: -42, Str: nil}
┌────────┬───────┬─────┐
│ TypeI64│  -42  │  —  │
└────────┴───────┴─────┘

Cell{Type: TypeStr, I64: 0, Str: "hello"}
┌────────┬─────┬─────────┐
│ TypeStr│  —  │ "hello" │
└────────┴─────┴─────────┘
```

---

## 2. Wire Formats (No Type Tag on the Wire)

Important: **the encoded bytes do not store `Type`**. The caller (later: the schema) must already know which type to decode.

### Integer (`TypeI64`) — 8 bytes LE

```text
int64 → cast to uint64 → LittleEndian 8 bytes

Example: -42
┌────────────────────────────────────────┐
│ D6 FF FF FF FF FF FF FF                │
└────────────────────────────────────────┘
  two's complement, little-endian
```

### String (`TypeStr`) — length prefix + bytes

```text
┌──────────┬─────────────────┐
│ str_len  │ str bytes       │
│ 4 B LE   │ N bytes         │
└──────────┴─────────────────┘

"hello" → [05 00 00 00 | h e l l o]
```

---

## 3. The `toAppend` Pattern (Encode)

```go
func (cell *Cell) Encode(toAppend []byte) []byte
```

Instead of `make` + return a brand-new slice every time, encode **appends** into an existing buffer. Building a whole row becomes one growing allocation:

```text
buf := []byte{}
buf = cell0.Encode(buf)   // grows
buf = cell1.Encode(buf)   // grows
buf = cell2.Encode(buf)   // one logical row blob
```

```text
Start:  []
After i64 1:   [01 00 00 00 00 00 00 00]
After str "x": [01 00 00 00 00 00 00 00 | 01 00 00 00 | x ]
```

---

## 4. The `rest` Pattern (Decode)

```go
func (cell *Cell) Decode(data []byte) (rest []byte, err error)
```

Decode consumes a prefix and returns the **unconsumed tail** so the next cell can continue:

```text
data:  [ i64 bytes | str header | str payload | ... ]
         └─ decode i64 ─┘
                         └─ rest passed to next Decode ─┘
```

| Call | Consumes | Returns |
| :--- | :--- | :--- |
| `Decode` on `TypeI64` | 8 bytes | `data[8:]` |
| `Decode` on `TypeStr` | 4+N bytes | `data[4+N:]` |

---

## 5. Endianness Note

| Use case | Typical endianness |
| :--- | :--- |
| Local CPU / this chapter’s cell payload | **Little-endian** (native on x86/ARM) |
| Network protocols / order-preserving index keys (0403) | **Big-endian** (MSB first for sort) |

0201 optimizes for native speed of value payloads, not index sort order.

---

## Crucial Takeaways

* `Cell` is the typed atom of the relational layer.
* Wire format is type-tag-free; schema supplies type knowledge.
* `Encode(toAppend)` and `Decode → rest` enable zero-waste row packing.
* Next: glue cells into **rows + schemas** mapped onto KV keys/values (0202).
