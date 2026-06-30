# Chapter 0201: Relational Data Types and Binary Representation

## Overview: Transitioning to the Relational Layer
Chapters 0101 through 0105 focused entirely on the **Storage Layer**: building a robust, atomic, and durable Key-Value (KV) engine that safely persists raw byte slices to physical disk sectors. 

This chapter begins the **Relational Layer**. We are shifting from raw storage to logical database constructs. Like Excel or PostgreSQL, a relational database contains tables, and those tables contain rows and columns. Unlike our raw KV store (which only understands unstructured `[]byte`), columns require strict **Data Types**. We will implement the fundamental architecture to support two types: `int64` and `[]byte` (Strings/Blobs).

---

## 1. The `Cell` Struct: Simulating Union Types in Go

To represent a single unit of data in a table row, we introduce the `Cell` struct. A cell must be capable of holding *either* an integer or a string.

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

### The "Wasted Space" Trade-off
In languages like C or C++, you would use a `union` to map both the integer and the string to the exact same memory address, saving RAM since a cell can only be one type at a time. Go does not have a native `union` type. 

By defining the `Cell` struct with fields for every possible data type, we accept a memory trade-off. If a `Cell` is initialized as a `TypeI64`, the `Str` byte slice field still occupies memory (a 24-byte slice header in Go) even though it is unused. While this wastes some RAM, it avoids the high performance overhead and complexity of using Go's `any` (empty interface) or `unsafe.Pointer` for type assertion.

---

## 2. Zero-Allocation Serialization Strategy

To save these `Cell` constructs to our KV engine, we must serialize them down to raw bytes. 

```go
func (cell *Cell) Encode(toAppend []byte) []byte
func (cell *Cell) Decode(data []byte) (rest []byte, err error)
```

### The `toAppend` Pattern (Write Optimization)
Notice that `Encode` does not return a newly allocated `[]byte`. Instead, it accepts an existing slice (`toAppend`) and appends its data to it. 
When a database serializes a row containing 50 columns, allocating a new byte slice for every single cell would trigger massive Garbage Collection (GC) pressure and destroy throughput. By passing a single, pre-allocated row buffer into each cell's `Encode` function, the entire row is serialized using a single memory allocation.

### The `rest` Pattern (Read Optimization)
Conversely, `Decode` takes a byte slice, extracts exactly the bytes it needs to populate the `Cell`, and returns the *remaining* unread bytes (`rest`). This allows a row decoder to cleanly chain `Decode` calls across a byte slice without manually tracking offset counters.

### Byte Format Specification
* **Integers (`int64`):** Stored exactly as 8 bytes.
* **Strings/Bytes (`[]byte`):** Stored dynamically. The first 4 bytes represent the length of the string as a `uint32`, followed immediately by the raw string data.

---

## 3. Memory Layout: Endianness Explained

When serializing an 8-byte integer to a disk, the physical order of those bytes matters. CPUs process binary numbers in fixed sizes (8, 16, 32, or 64 bits). 

Take the 32-bit hexadecimal number `0x11223344`. In a CPU register, it is a single value. But when writing it to RAM or a disk, it must be split into 4 separate bytes: `11`, `22`, `33`, and `44`. How are they ordered in memory?

* **Big Endian (Written Order):** The Most Significant Byte (the "biggest" part of the number, `11`) is stored at the lowest memory address. 
    * Memory Layout: `11 22 33 44`
    * *Use Case:* Historically used by older CPUs. Still the standard for network protocols (TCP/IP) and heavily utilized in databases for **lexicographical sorting** (e.g., B-Tree index traversal), which we will exploit later.
* **Little Endian (Natural Order):** The Least Significant Byte (the "smallest" part of the number, `44`) is stored at the lowest memory address.
    * Memory Layout: `44 33 22 11`
    * *Use Case:* The dominant architecture for modern CPUs (x86, ARM). Because modern hardware natively uses Little Endian, standardizing our disk formats on Little Endian avoids the CPU overhead of reversing byte orders during read/write operations.

---

## 4. Binary Interpretation: Signed vs. Unsigned Integers

Go's standard library (`binary.LittleEndian`) provides methods for serializing `uint64` (unsigned integers), but notably lacks methods for `int64` (signed integers). 

This is not a missing feature; it highlights a fundamental truth about computer architecture: **at the hardware level, there is no difference between a signed and unsigned integer.** The difference is entirely in how the CPU (or the compiler) *interprets* those exact same bits.

Because the binary representation is identical, we can perform a direct type cast without any data conversion logic:
```go
cell.I64 = int64(binary.LittleEndian.Uint64(data[0:8]))
```

### Two's Complement Architecture
Many developers assume negative numbers are stored using a "Sign + Absolute Value" method (e.g., flipping the highest bit to 1 to mean negative). This is mathematically inefficient and creates a paradox where a computer has both a `+0` and a `-0`.

Modern systems use **Two's Complement**. 
In an 8-bit unsigned system, the range is 0 to 255. 
To support signed numbers, the system simply splits that range in half:
* `0` through `127` represent positive numbers.
* `128` through `255` are mapped to represent `-128` through `-1`.

Converting between `uint64` and `int64` requires zero CPU cycles. It is simply a directive to the compiler on how to evaluate the mathematical limits of the bits sitting in the register.
