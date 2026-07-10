# Chapter 0305: Execute SQL

## Systems Engineering Overview

At this stage in the engine's lifecycle, we are constructing the **Query Execution Engine**—the critical middleware that bridges the frontend parsing phase with the backend storage engine. Prior to this chapter, the system possessed a raw Key-Value storage layer and a recursive descent parser that produced Abstract Syntax Trees (ASTs). The execution layer acts as a command multiplexer, decoupling the logical intent of a query from its physical implementation.

The primary entry point is the router mechanism (`ExecStmt`), which consumes a typed AST node (e.g., `*StmtSelect`, `*StmtInsert`) and delegates it to the appropriate execution pipeline. To ensure a uniform database API, all pipelines collapse their outputs into a single, standardized I/O boundary: the `SQLResult` struct. This unified data structure elegantly handles both scalar mutations (tracking the integer count of affected rows for DML operations) and vector/matrix retrievals (returning the header and row data for DQL operations), abstracting the underlying storage mechanics away from the client caller.

## Architecture

The execution layer is heavily dependent on a dual-tier metadata architecture and distinct execution pipelines.

### The Schema Registry (Metadata Layer)
All relational operations depend on table schemas to map logical columns to physical byte offsets. The engine introduces a schema registry with two distinct layers:
* **Durable Storage (Disk/KV):** Schemas are serialized into JSON and stored durably in the underlying Key-Value engine using a reserved namespace prefix (`@schema_` + table name). This ensures metadata persists across database restarts.
* **Volatile Storage (RAM Cache):** To prevent severe I/O bottlenecks where every query requires a disk read to fetch schema metadata, the `DB` struct implements a memory-resident cache (`tables map[string]Schema`). This cache lazily populates on the first query to a table and serves all subsequent requests directly from RAM.

### Execution Pipelines
The multiplexer routes AST nodes into three primary operational pipelines:
* **DDL Pipeline (Create):** Transforms the `StmtCreatTable` AST into a physical `Schema` object, serializes it, persists it to the KV store, and actively populates the RAM cache.
* **DQL Pipeline (Select):** A multi-stage retrieval and filtering pipeline. It resolves requested column names to schema indices (`lookupColumns`), builds a physical lookup key from the `WHERE` clause (`makePKey`), executes the physical KV `Select`, and finally slices the retrieved data to match the requested projection (`subsetRow`).
* **DML Pipeline (Insert, Update, Delete):** Maps AST values to physical rows and invokes the previously established backend storage routines (`DB.Insert`, `DB.Update`, `DB.Delete`), returning the mutation count to populate the `SQLResult`.

## CPU/Memory Layout

The implementation of the execution layer introduces new memory lifecycle considerations for the engine:

* **The Cache Map (`tables map[string]Schema`):** This introduces a persistent memory footprint. While a single schema is lightweight, holding all accessed schemas in heap memory trades RAM for CPU/IO efficiency. Because schemas are relatively static, cache invalidation is not yet a primary concern, but the map itself represents long-lived allocations.
* **Result Set Allocations (`Values []Row`):** For `SELECT` operations, the engine must allocate slices to hold the result matrix. Currently, operations like `subsetRow` create intermediate allocations in memory to filter the physical row down to the requested projection. In a high-throughput environment, this intermediate slice allocation per row can trigger heavy Garbage Collection (GC) pressure. Future optimizations may require zero-copy slicing or arena allocators to manage this memory churn.

## System Constraints

By pushing constraints down to the lowest possible layers, we establish strict boundaries for the current iteration of the engine:

* **Primary Key Immutability:** The `UPDATE` execution pipeline explicitly forbids the modification of primary keys. Physically, a relational update to a primary key is not a true update in a KV store; it requires a logical `DELETE` of the old key-value pair and an `INSERT` of the new one. By constraining `UPDATE` to only modify the value payload, we avoid the heavy I/O and potential fragmentation overhead of key deletion and recreation.
* **Exact-Match Indexing Limitation:** The `makePKey` helper function currently implies exact-match indexing. Because the execution layer relies on fully constructed keys to query the underlying storage, the engine is temporarily constrained to point queries (e.g., `WHERE id = 5`). Range scans (e.g., `WHERE id > 5`) or full table scans are not supported until a cursor or iterator mechanism is implemented at the KV layer.
