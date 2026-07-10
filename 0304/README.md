# Chapter 0304: Statements

## Systems Engineering Overview
The transition in this chapter elevates the engine from a single-statement processor (handling only `SELECT`) into a multi-statement relational database router. To achieve this, the author implements a centralized dispatch mechanism using a classic LL(k) recursive descent parsing strategy. 

By upgrading the tokenizer to look ahead just one or two tokens, the parser can definitively classify incoming Data Definition Language (DDL) and Data Manipulation Language (DML) commands without relying on heavy compiler-construction theories or abstract grammar tools. This keeps the parsing layer tightly coupled, lightweight, and perfectly suited to the engine's current constraints.

## Architecture
The architectural delta introduces a root statement router and the memory structures required to represent the parsed SQL commands as Abstract Syntax Tree (AST) nodes.

**1. AST Node Structures (The Schemas)**
Once the statement type is identified, the parser populates a specific struct to hold the extracted tokens. These types map directly to the required KV storage operations:

```go
type StmtCreatTable struct {
	table string
	cols  []Column
	pkey  []string
}

type StmtInsert struct {
	table string
	value []Cell
}

type StmtUpdate struct {
	table string
	keys  []NamedCell
	value []NamedCell
}

type StmtDelete struct {
	table string
	keys  []NamedCell
}
```

**2. The Dispatcher (`parseStmt`)**
This function is the new entry point for execution. It utilizes an upgraded `tryKeyword` function that accepts variadic arguments (`kws ...string`), allowing the parser to consume multi-word SQL commands (like `CREATE TABLE`) as a single logical branch condition.

```go
func (p *Parser) parseStmt() (out interface{}, err error) {
	if p.tryKeyword("SELECT") {
		stmt := &StmtSelect{}
		err = p.parseSelect(stmt)
		out = stmt
	} else if p.tryKeyword("CREATE", "TABLE") {
		stmt := &StmtCreatTable{}
		err = p.parseCreateTable(stmt)
		out = stmt
	} else if p.tryKeyword("INSERT", "INTO") {
		stmt := &StmtInsert{}
		err = p.parseInsert(stmt)
		out = stmt
	} else if p.tryKeyword("UPDATE") {
		stmt := &StmtUpdate{}
		err = p.parseUpdate(stmt)
		out = stmt
	} else if p.tryKeyword("DELETE", "FROM") {
		stmt := &StmtDelete{}
		err = p.parseDelete(stmt)
		out = stmt
	} else {
		err = errors.New("unknown statement")
	}
	
	if err != nil {
		return nil, err
	}
	return out, nil
}
```

## CPU/Memory Layout
* **Polymorphic Heap Escapes:** `parseStmt()` returns an `interface{}`. In Go, an empty interface is represented in memory as a two-word structure: one pointer to the type descriptor (e.g., `*StmtInsert`) and one pointer to the actual data. Returning pointers to structs (like `&StmtInsert{}`) through an interface forces these AST nodes to escape the stack and allocate on the heap. This is an acceptable memory trade-off for a unified parsing pipeline.
* **Dynamic Slices:** The AST structs heavily utilize Go slices (`[]Column`, `[]string`, `[]Cell`, `[]NamedCell`). Because the arity of the incoming SQL queries (e.g., number of columns in an insert) is unknown at compile time, these slices will dynamically allocate and potentially reallocate backing arrays on the heap as the parser loops through the tokens.

## System Constraints
* **Single-Row Point Lookups:** The engine's mutation capabilities are strictly constrained. The `WHERE` clause logic currently maps directly to the `keys []NamedCell` slice in the update and delete structs. This rigidly limits updates and deletes to single-row operations using exact Primary Key matches.
* **No Range Scans or Indexing:** Because we are forcing point-lookups on the primary key, complex predicates (e.g., `>`, `<`, `!=`), secondary index lookups, and range queries are completely unsupported at this layer.
