# Chapter 0305: Execute SQL

## Systems Engineering Overview
We are building the **Query Execution Engine**—the traffic controller of our database. Until now, we could parse queries (understand the command) and store data (write to disk). This new execution layer connects the two. 

The main router, `ExecStmt`, looks at a parsed query (like a `SELECT` or `INSERT`) and sends it to the correct execution pipeline. No matter what the operation is, the result is always handed back to the user in a single, standardized package called `SQLResult`. This keeps our database API completely unified.

## Architecture
The execution layer relies on two main mechanisms to process queries:

* **The Schema Registry (Metadata):** Before doing anything, the engine needs to know what the tables look like. 
    * **Disk (Durable):** Schemas are saved permanently as JSON in the Key-Value store under a special `@schema_` prefix so they survive reboots.
    * **RAM Cache (Volatile):** Reading from disk every time is too slow. We keep a fast copy of accessed schemas in memory (`tables map[string]Schema`) so future queries are practically instant.
* **Execution Pipelines:** * **Create (DDL):** Builds a new schema and saves it to both disk and the RAM cache.
    * **Select (DQL):** Finds the exact primary key, fetches the physical row, and slices off only the specific columns the user asked for.
    * **Insert/Update/Delete (DML):** Modifies the exact physical bytes on the disk and returns the number of rows that were successfully changed.

### 1. DDL (Data Definition Language)
**Purpose:** Defines the physical rules, schemas, and structural blueprints of the database.
* **Operations:** `CREATE TABLE`
* **Pipeline Mechanics:** When a DDL statement is executed, the engine allocates physical indices for the requested columns and establishes the primary key constraints. This blueprint (the Schema) is serialized into JSON and written to the durable KV store using a metadata prefix (e.g., `@schema_tablename`). It is simultaneously cached in a RAM map to ensure zero-latency schema lookups during future read/write operations.

### 2. DML (Data Manipulation Language)
**Purpose:** Modifies the physical user data stored within the tables.
* **Operations:** `INSERT`, `UPDATE`, `DELETE`
* **Pipeline Mechanics:** DML acts as a strict memory formatter and mutation layer.
    * **INSERT:** Operates as a linear safety checkpoint. It assumes the parser has aligned the input, verifies column counts, enforces strict type safety against the schema, allocates a physical memory slice, and delegates to the KV engine to encode the slice into raw `[]byte` sequences for the physical disk log.
    * **UPDATE:** Executes a strict "Read-Modify-Write" cycle. It translates the `WHERE` clause into a physical disk address, fetches and decodes the existing byte array, applies mutations in RAM (while actively blocking any attempts to modify primary key indices), and overwrites the sequence on disk.
    * **DELETE:** Formats the target constraint into a physical primary key and executes a "tombstone" write. It appends a marker to the physical disk log indicating the record is deleted and instantly purges the key from the fast-access RAM map.

### 3. DQL (Data Query Language)
**Purpose:** Retrieves and reshapes data from the database without altering the state.
* **Operations:** `SELECT`
* **Pipeline Mechanics:** DQL acts as the ultimate translator between the user's abstract projection requests and the physical hardware. It fetches the schema blueprint, converts requested column strings into exact integer memory offsets, and translates the `WHERE` clause into a physical key for the KV lookup. Once the physical byte array is fetched and decoded from the disk, the engine meticulously slices off any unrequested column data, returning a perfectly trimmed 2D matrix back to the client.

## CPU/Memory Layout
* **The Cache Map:** Keeping schemas in memory (`tables map[string]Schema`) trades a little bit of RAM for a massive speed boost in CPU and I/O efficiency. Since table structures don't change often, this is a highly efficient long-term memory allocation.
* **Result Set Allocations:** When a `SELECT` query runs, slicing out specific columns creates temporary memory allocations. In a high-traffic database, creating and destroying these slices constantly will force Go's Garbage Collector to work overtime.

## System Constraints
* **Primary Key Immutability:** You cannot change a primary key using an `UPDATE` statement. In our KV store, the primary key is the literal physical address of the data. Changing it requires a costly logical `DELETE` followed by a new `INSERT`, so we strictly limit updates to the row's values only.
* **Exact-Match Limitation:** Because of how our physical lookups work right now, `SELECT` queries are temporarily limited to exact primary key matches (e.g., `WHERE id = 5`). Range scans (e.g., `WHERE id > 5`) are not supported until we build iterator mechanics later down the line.


## Syntax, functions..
* json.Unmarshal: In Go, this is the primary function in the encoding/json package used to decode JSON data into Go data structures. It takes a JSON byte slice and a pointer to the target variable, automatically allocating maps, slices, and pointers as necessary. 

* json.Marshal: In Go, this is the process of converting GO data structures into JSON format using this function from the standard encoding/json package. 
