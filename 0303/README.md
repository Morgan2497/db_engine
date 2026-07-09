### 1. Systems Engineering Overview
Chapter 0303 bridges the critical gap between lexical tokenization (identifying raw bytes) and Abstract Syntax Tree (AST) construction (enforcing grammatical logic). The engine restricts the `SELECT` execution path to a highly specialized, deterministic pattern: a single-row point query by Primary Key. This intentional limitation guarantees that the parsed `StmtSelect` struct maps directly to a high-speed Key-Value storage lookup at the Storage Layer (0101), completely bypassing complex query planning, full table scans, or relational joins at this stage of the engine's lifecycle.

### 2. Architecture / Core Implementation
The structural core relies on mapping SQL strings into tightly packed structs. The design explicitly binds parsed values directly into the `Cell` primitives defined in the underlying storage system.

```go
type StmtSelect struct {
    table string
    cols  []string
    keys  []NamedCell
}

type NamedCell struct {
    column string
    value  Cell // Binds directly to the disk-ready Cell primitive
}
```

- Since only a small set of features has been implemented, our SELECT statement supports
only one fixed form: query a single row by primary key. For example, for a table with
primary key (c, d), the only supported query is this:

```
select a,b from t where c=1 and d='e';
```

```
```
- Represented as data structures:

``` 
StmtSelect{
  table: "t",
  cols: []string{"a", "b"},
  keys: []NamedCell{
    {column: "c", value: Cell{Type: TypeI64, I64: 1}},
    {column: "d", value: Cell{Type: TypeStr, Str: []byte("e")}},
  },
}
```

The parsing functions act as state-mutating routers:
* **`parseEqual(out *NamedCell)`**: Combines `tryName`, `tryPunctuation`, and `parseValue`. It enforces strict `ident = literal` formatting.
* **`parseSelect(out *StmtSelect)`**: The orchestrator. Extracts the target columns, the source table, and delegates the filter logic.
* **`parseWhere(out *[]NamedCell)` (Derived Implementation)**: The implementation must enforce a strict `ident = literal AND ident = literal` sequence without unbounded recursion:

```go
func (p *Parser) parseWhere(out *[]NamedCell) error {
    if !p.tryKeyword("WHERE") {
        return nil // Optional, or error if PK lookup is strictly enforced
    }
    for {
        var cell NamedCell
        if err := p.parseEqual(&cell); err != nil {
            return err
        }
        *out = append(*out, cell)
        
        if !p.tryKeyword("AND") {
            break // Exit loop when no more AND tokens exist
        }
    }
    return nil
}
```

### 3. CPU Mechanics & Memory Layout
While lexical functions like `parseInt` are strictly zero-allocation, this syntactic layer introduces mandatory heap allocations. Slices like `cols []string` and `keys []NamedCell` will trigger heap allocations as they grow via `append`. However, string extraction via `tryName()` still leverages underlying byte slice pointer math to minimize extraneous string building. 

The parser operates as an LL(1) predictive parser. It avoids backtracking; if `tryKeyword` or `tryPunctuation` fails, the memory cursor (`p.pos`) is not advanced. This allows for immediate fallback or error generation without thrashing memory buffers or requiring complex state rewinding.

### 4. System Constraints & Boundaries
This parser acts as a rigid, uncompromising funnel. It strictly rejects anything outside the exact format: `SELECT col1, col2 FROM table WHERE pk1 = val1 AND pk2 = val2`. It assumes that the underlying Relational Bridge (0201) is configured to handle composite primary keys exactly in the order they are parsed. There is zero tolerance for `OR` conditions, parentheses, or aggregate functions. The populated `StmtSelect` struct is the absolute boundary between the compiler front-end and the execution back-end.

```
```
```
```StmtSelect{
	table: "t",
	cols: []string{"a", "b"},
	keys: []NamedCell{
		{
			column: "c",
			value: Cell{
				Type: TypeI64,
				I64:  1,
				Str:  nil,
			},
		},
		{
			column: "d",
			value: Cell{
				Type: TypeStr,
				I64:  0,
				Str:  []byte{'e'}, // The byte representation of 'e'
			},
		},
	},
}
