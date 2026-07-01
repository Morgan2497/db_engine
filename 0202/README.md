# Chapter 0202: Table Schema and Row Serialization

## Overview: Logical Intelligence over Physical Storage
In Chapter 0201, we established the **Cell** as our unit of data. In this chapter, we transition from handling individual cells to managing complete **Tables**. We are now building the **Relational Layer** that maps our physical Key-Value storage to logical, schema-aware entities.

Our storage engine is becoming a true Relational Database (OLTP). We must now define the blueprint (Schema) for how rows are structured, identified, and partitioned into Keys and Values.

---

## 1. The Schema Definition
The `Schema` is the metadata that defines the identity and composition of a table. It dictates how raw `[]byte` should be interpreted back into meaningful columns.

```go
type Column struct {
    Name string
    Type CellType
}

type Schema struct {
    Table string
    Cols  []Column
    PKey  []int // Column indices forming the primary key
}
```

* **Primary Key (`PKey`):** A slice of integer indices pointing to specific columns in `Cols`. This forms the unique identifier (the "K" in KV) for a given row.
* **Column Order:** The order of columns in `Cols` is invariant. Serialization and deserialization must strictly adhere to this order to maintain data integrity.

---

## 2. Row Representation and Memory Management
We define a `Row` as a slice of `Cell` objects. To maintain the **Zero-Allocation** performance pattern established previously, we avoid dynamic resizing by pre-allocating the row based on the schema.

```go
type Row []Cell

func (schema *Schema) NewRow() Row {
    return make(Row, len(schema.Cols))
}
```

### The "Primary Key = K, Remaining = V" Strategy
To implement a relational engine on a KV store, we perform a functional split during serialization:

1.  **`EncodeKey`**: Encodes only the columns defined in `schema.PKey`. This acts as the physical address.
2.  **`EncodeVal`**: Encodes all columns **not** present in `schema.PKey`. This acts as the data payload.

---

## 3. Key Prefixing and Namespace Isolation
Since our KV store hosts multiple tables, we must prevent key collisions. If we have a table `ab` and a table `abc`, a naive key prefix would cause significant conflict.

### The Null-Byte Separator
We enforce namespace isolation by appending a `0x00` byte after the table name.

```go
func (row Row) EncodeKey(schema *Schema) (key []byte) {
    key = append([]byte(schema.Table), 0x00)
    // Append Primary Key cells here
}
```

* **Conflict Resolution:** With this separator, `ab` becomes `ab\x00...` and `abc` becomes `abc\x00...`. They will never overlap in the underlying storage.
* **Production Note:** While we currently use string-based prefixes for clarity, migrating to **integer-based Table IDs** is the logical next step to reduce storage overhead and allow for table renaming without rewriting physical data.

---

## 4. Indexing: The "Book" Analogy
In our system, the Primary Key is mandatory and functions as the direct pointer to the row data. Secondary indexes are effectively separate KV stores that map custom fields to the Primary Key.

* **Primary Key (K):** The "Page Number." Essential for accessing the actual record.
* **Secondary Index:** The "Table of Contents." An auxiliary structure used to find the Page Number.
* **Optimization:** In some scenarios, the index contains enough information to satisfy a query entirely, eliminating the need to read the primary value payload (a "Covering Index" operation).

---

## 5. Implementation Requirements & Constraints
* **Matching Schema:** Every `Row` must have a length exactly equal to the length of the `Schema.Cols` slice.
* **Reuse of `Cell` Logic:** We rely on the `Cell.Encode()` and `Cell.Decode()` methods from Chapter 0201.
* **Data Integrity:** During `Decode`, we must ensure that the incoming byte stream matches the expected `CellType` defined in the `Schema`. Mismatches here indicate either data corruption or a schema versioning failure.


* EX:
create table `link` (
	`time` int64 not null,
	`src` string not null,
	`dst` string not null,
	primary key (`src`, `dst`)
);

- is represented as 
schema := &Schema{
	Table: "link",
	Cols: []Column{
		{Name: "time", Type: TypeI64},
		{Name: "src", Type: TypeStr},
		{Name: "dst", Type: TypeStr},
	},
	PKey: []int{1, 2}, // (src, dst)
}

row := Row{
    // Item 1
    {Type: TypeI64, I64: 1700000000, Str: nil},       

    // Item 2
    {Type: TypeStr, I64: 0, Str: []byte("nodeA")},    

    // Item 3
    {Type: TypeStr, I64: 0, Str: []byte("nodeB")},    
}


Iteration 1 (idx = 0 : time)

check(TypeI64 == TypeI64): Passes.

slices.Contains([1, 2], 0): False. * Action: Since column 0 is not part of the Primary Key, it is ignored entirely.

Memory State: Unchanged. [ 'l', 'i', 'n', 'k', 0x00 ]

Iteration 2 (idx = 1 : src)

check(TypeStr == TypeStr): Passes.

slices.Contains([1, 2], 1): True.

Action: The engine calls row[1].Encode(key). As built in Chapter 01, this appends the string length (4 bytes, LittleEndian) followed by the raw string bytes ("nodeA").

Memory State of key: [ 'l', 'i', 'n', 'k', 0x00,  (prefix)
0x05, 0x00, 0x00, 0x00,  (length of "nodeA" as uint32)
'n', 'o', 'd', 'e', 'A' ] (raw string)

Iteration 3 (idx = 2 : dst)

check(TypeStr == TypeStr): Passes.

slices.Contains([1, 2], 2): True.

Action: The engine calls row[2].Encode(key). It appends the length (4 bytes) and raw bytes of "nodeB" to the existing slice, utilizing the Zero-Allocation pattern.

Memory State of key: [ 'l', 'i', 'n', 'k', 0x00,  (prefix)
0x05, 0x00, 0x00, 0x00, 'n', 'o', 'd', 'e', 'A',  (src)
0x05, 0x00, 0x00, 0x00, 'n', 'o', 'd', 'e', 'B' ] (dst)






