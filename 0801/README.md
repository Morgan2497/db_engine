# Chapter 0801: Indexes and KV

## The Main Idea

An index is an additional data structure that helps a database find rows
without scanning every row in a table.

Our database already has one index-like structure: the primary-key KV store.
Chapter 0801 extends the schema so a table can describe more than one index.

```text
before 0801:
Schema.PKey -> one primary-key definition

after 0801:
Schema.Indices[0] -> primary-key definition
Schema.Indices[1] -> first secondary index
Schema.Indices[2] -> second secondary index
...
```

This chapter does not create secondary-index KV entries or use them in a
query. It only:

- parses `INDEX (...)` inside `CREATE TABLE`;
- stores all index definitions in `Schema.Indices`;
- places the primary key at `Indices[0]`; and
- appends primary-key columns to every secondary index.

Maintaining index entries comes in 0802. Querying them comes in 0803.

---

## A Better Mental Model: Three Independent Questions

The word “index” is used for several related decisions. They are easier to
understand when separated into three questions.

### Question 1: What does the query search by?

```text
primary index       -> unique row identity
secondary index     -> another row attribute
multi-column index  -> an ordered tuple such as (last_name, first_name)
multidimensional    -> a region across independent dimensions
fuzzy/full-text     -> similar words or documents rather than exact keys
```

This question describes the index's **search semantics**.

### Question 2: What does an index entry give back?

```text
key -> heap-file row identifier
key -> primary key
key -> complete row
key -> row reference plus included columns
```

This question describes the index's **payload and row-storage relationship**.
It distinguishes heap/nonclustered, clustered, and covering designs.

### Question 3: Which data structure implements it?

```text
B-tree
LSM-tree
hash table
R-tree
trie or finite-state structure
```

This question describes the index's **physical implementation**.

These categories are not competing alternatives. They can be combined. For
example, one database could have:

```text
a secondary + multi-column + covering + LSM-tree index
```

That means:

```text
secondary     -> it is not the primary-key lookup
multi-column  -> its search key contains several ordered fields
covering      -> it stores extra columns needed by a query
LSM-tree      -> its physical updates and reads use LSM levels
```

This separation prevents a common misunderstanding: “clustered,”
“secondary,” and “multi-column” do not name three mutually exclusive index
types. Each describes a different aspect of the same index.

### B-tree versus LSM-tree is a different question

A B-tree or LSM-tree tells us how the index is physically maintained. Primary
or secondary tells us what the index is used to search.

```text
B-tree / LSM-tree
        |
        +-- How is this index stored and updated?

primary / secondary / multi-column
        |
        +-- What information does this index organize?
```

Both B-trees and LSM-trees can implement primary and secondary indexes. An
LSM-tree often turns random updates into sequential writes but may need to
check multiple levels. A B-tree normally keeps a logical key in one current
location and often offers more predictable point-read behavior. The right
choice depends on the workload and should be measured rather than inferred
from the index label alone.

---

## 1. What Is an Index?

Consider this table:

```text
users

id   name    city       age
--------------------------------
10   Alice   Seattle    31
20   Bob     Portland   25
30   Carol   Seattle    40
40   David   Seattle    25
```

Without an index, this query may scan every row:

```sql
SELECT * FROM users WHERE city = 'Seattle';
```

```text
check row 10 -> match
check row 20 -> no
check row 30 -> match
check row 40 -> match
```

An index on `city` creates another searchable view:

```text
Portland -> row 20
Seattle  -> rows 10, 30, 40
```

The table stores the logical rows. The index stores information that helps
locate those rows.

An index is not free. It improves the queries that match its key order, but it
also consumes storage and must be updated whenever relevant data changes.

---

## 2. Primary-Key Index

A primary key uniquely identifies one row:

```text
id 10 -> exactly one row
id 20 -> exactly one row
id 30 -> exactly one row
id 40 -> exactly one row
```

This resembles the key-value interface already used by the database:

```text
unique key -> row value

10 -> Alice, Seattle, 31
20 -> Bob, Portland, 25
```

The primary key is both:

- the row's identity; and
- the lookup key for retrieving that row.

Another row cannot reuse the same primary key.

```sql
INSERT INTO users VALUES (10, 'Eve', 'Boston', 29);
```

This cannot create a second row with `id = 10`.

---

## 3. Secondary Index

A secondary index provides another way to locate the same rows.

```sql
INDEX (city)
```

Unlike a primary key, a secondary-index value usually is not unique:

```text
Seattle appears in rows 10, 30, and 40
```

There are two common ways to represent these duplicates.

### Option A: One key with a list of row identifiers

```text
Portland -> [20]
Seattle  -> [10, 30, 40]
```

The list of matching identifiers is often called a posting list. A query
looks up `Seattle`, obtains the identifiers, and then retrieves those rows.

### Option B: Append the primary key

Make each secondary-index entry unique by including the row's primary key:

```text
(Portland, 20) -> empty
(Seattle, 10)  -> empty
(Seattle, 30)  -> empty
(Seattle, 40)  -> empty
```

This project uses the second approach.

The primary key performs two jobs in each secondary index:

1. It makes the KV key unique.
2. It identifies the primary row associated with the entry.

Because the entries are sorted, every entry beginning with `Seattle` remains
next to the others:

```text
(Seattle, 10)
(Seattle, 30)
(Seattle, 40)
```

A range scan over the `Seattle` prefix can therefore find all matching rows.

---

## 4. Why the Secondary Index Needs the Primary Key

Our KV store supports only one value for each key. These entries cannot all
exist independently:

```text
Seattle -> empty
Seattle -> empty
Seattle -> empty
```

Each later `Set("Seattle", ...)` would replace the previous entry.

Appending the primary key produces unique keys:

```text
Seattle + 10
Seattle + 30
Seattle + 40
```

It also gives the future query path:

```text
secondary entry: (Seattle, 30)
                         |
                         v
extract primary key: 30
                         |
                         v
primary-index lookup
                         |
                         v
Carol's complete row
```

Chapter 0801 prepares this representation in the schema. Chapter 0802 will
encode and maintain the actual entries.

---

## 5. What Can an Index Store as Its Value?

The index key is what a query searches. The value can contain the full row or
a reference to a row stored somewhere else.

### Heap-file reference

A heap file stores rows without requiring key order. Here, “heap” means an
unordered record-storage file, not the heap memory data structure.

A row location is often represented by a **row identifier**, abbreviated
`RID`. A simplified identifier might contain a page number and a slot number:

```go
type RID struct {
	PageID uint32
	SlotID uint16
}
```

The page identifies a storage page; the slot identifies a record within that
page.

```text
heap location 1000 -> Alice, Seattle, 31
heap location 1140 -> Bob, Portland, 25
heap location 1290 -> Carol, Seattle, 40
```

Indexes can point to those locations:

```text
primary index:
10 -> location 1000
20 -> location 1140
30 -> location 1290

city index:
(Seattle, 10)  -> location 1000
(Portland, 20) -> location 1140
(Seattle, 30)  -> location 1290
```

The read path has an extra hop:

```text
index lookup -> heap-file location -> actual row
```

The advantage is that the full row exists in one place even when the table has
several indexes.

### Updating a heap-file row

If a new row value fits in the old space, the database may overwrite it in
place. Index references remain valid.

If the new value is larger, the row may need to move. The database must then
either:

- update every index to the new location; or
- leave a forwarding pointer at the old location.

Updating every index increases write work. A forwarding pointer preserves the
old references but adds another read hop.

---

## 6. Clustered Index

A clustered index stores the actual row with the index entry:

```text
10 -> Alice, Seattle, 31
20 -> Bob, Portland, 25
30 -> Carol, Seattle, 40
40 -> David, Seattle, 25
```

The read path is shorter:

```text
primary-index lookup -> actual row
```

In MySQL InnoDB, the primary-key index is clustered. Its secondary indexes use
the primary key as the reference to the complete row rather than storing a heap
file location.

Conceptually:

```text
secondary city index:
(Portland, 20) -> primary key 20
(Seattle, 10)  -> primary key 10
(Seattle, 30)  -> primary key 30
(Seattle, 40)  -> primary key 40
```

Retrieving Carol through that secondary index requires:

```text
1. Find (Seattle, 30) in the city index.
2. Extract primary key 30.
3. Look up 30 in the clustered primary index.
4. Read Carol's row.
```

Only one index normally determines the primary physical row order, which is
why a table normally has only one clustered index.

---

## 7. Nonclustered and Covering Indexes

A nonclustered index stores a row reference rather than the complete row:

```text
(Seattle, 30) -> heap location or primary key
```

It saves space but usually requires another lookup to retrieve the row.

A covering index stores selected extra columns so a particular query can be
answered without retrieving the complete row.

For example:

```sql
SELECT name
FROM users
WHERE city = 'Seattle';
```

A normal city index might contain:

```text
(city, id) -> empty
```

It finds matching IDs, but the database must fetch each row to read `name`.

A covering index could contain:

```text
(city, id) -> name
```

```text
(Seattle, 10) -> Alice
(Seattle, 30) -> Carol
(Seattle, 40) -> David
```

The index now contains everything that query needs.

It does not cover this query if `age` is absent:

```sql
SELECT name, age
FROM users
WHERE city = 'Seattle';
```

Covering indexes trade additional storage and write work for fewer reads.

---

## 8. The Cost of Additional Indexes

Suppose Alice moves from Seattle to Boston:

```sql
UPDATE users
SET city = 'Boston'
WHERE id = 10;
```

The primary row changes:

```text
10 -> Alice, Boston, 31
```

The database must also remove:

```text
(Seattle, 10)
```

and add:

```text
(Boston, 10)
```

More indexes therefore mean:

- more disk space;
- more writes during inserts;
- more work during deletes;
- more work when indexed values change; and
- more structures that transactions must keep consistent.

If the database updates the row but crashes before updating the index, the two
structures disagree. Later transaction chapters will ensure readers observe
either the complete old state or the complete new state.

---

## 9. Multi-Column Indexes

A multi-column, or concatenated, index combines several column values into one
ordered key.

```sql
INDEX (last_name, first_name)
```

Its entries are ordered like a telephone directory:

```text
(Adams, Amy)
(Adams, Zoe)
(Brown, Alice)
(Brown, David)
(Smith, Alice)
(Smith, John)
```

The database first sorts by `last_name`. It sorts by `first_name` only among
rows with the same last name.

### Search using both columns

```sql
WHERE last_name = 'Smith'
  AND first_name = 'Alice'
```

The database can seek directly to:

```text
(Smith, Alice)
```

### Search using the first column

```sql
WHERE last_name = 'Smith'
```

This is also efficient because all Smith entries form one continuous range:

```text
(Smith, Alice)
(Smith, John)
```

### Search using only the second column

```sql
WHERE first_name = 'Alice'
```

This index cannot efficiently seek to one range because Alice entries may be
spread across many last names:

```text
(Brown, Alice)
(Smith, Alice)
(Williams, Alice)
```

This is the leftmost-prefix rule:

```text
index: (last_name, first_name)

efficient search prefixes:
  (last_name)
  (last_name, first_name)

not an efficient seek:
  (first_name) alone
```

Column order is therefore part of the index design, not a cosmetic choice.

---

## 10. Concatenated Indexes Are Still One-Dimensional

Consider:

```text
INDEX (latitude, longitude)
```

This index sorts latitude first and longitude second. It can efficiently find
a latitude range, but within that range it may encounter every longitude.

Reversing the columns solves the longitude search but creates the same problem
for latitude.

A normal ordered index arranges keys along one line:

```text
smaller ------------------------------------------ larger
```

A geographic query describes a two-dimensional rectangle:

```text
latitude
   ^
   |
   |        +----------------+
   |        | requested area |
   |        +----------------+
   |
   +--------------------------------> longitude
```

Independent range restrictions on two dimensions cannot generally become one
continuous lexicographic key range.

Two common alternatives are:

- a space-filling curve that maps coordinates to sortable one-dimensional
  values; and
- a spatial structure such as an R-tree that indexes bounding rectangles.

Multidimensional indexes are useful beyond geography. Example dimensions
include `(date, temperature)`, `(red, green, blue)`, and `(price, rating)`.

These structures provide useful context but are outside this project's 0801
implementation.

---

## Beyond Exact and Range Search: Fuzzy Indexes

The indexes in this project answer exact and ordered-range questions:

```text
exact: key == target
range: start <= key <= stop
```

A fuzzy index answers a different question:

```text
similarity(key, target) is close enough
```

For example, a user may search for the misspelling:

```text
databse
```

while the indexed term is:

```text
database
```

A hash index treats those as unrelated keys. A normal ordered index also sees
two different strings. A fuzzy search can use edit distance—the number of
insertions, deletions, or substitutions needed to turn one term into another.

Full-text systems may combine term dictionaries, trie-like or finite-state
structures, and algorithms such as Levenshtein automata to find terms within
an allowed edit distance.

This is not just another secondary B-tree. It supports different search
semantics:

```text
ordinary ordered index -> equality and range
multidimensional index -> overlap in several dimensions
fuzzy index            -> approximate similarity
```

No single index structure efficiently supports every query type. Fuzzy and
full-text indexing are intentionally outside the scope of Chapter 0801.

---

## In-Memory Databases Are an Architectural Choice

An in-memory database is not another category beside primary, secondary, or
covering indexes. It answers a different architectural question:

```text
Where does the active database state live?
```

An in-memory database keeps its working structures in RAM. It may still use a
write-ahead log, snapshots, or replication for durability:

```text
active indexes and rows -> RAM
durability              -> log and/or snapshots
```

This distinction matters because many B-tree and LSM-tree details exist to
manage disk behavior. Keeping data in memory changes the performance trade-offs
but does not eliminate the need for indexing, durability, or recovery.

An in-memory database can still have primary, secondary, multi-column, or
covering indexes. “In-memory” describes storage placement; those other terms
describe search and row layout.

---

## 11. SQL Syntax Added in 0801

The chapter supports index declarations inside `CREATE TABLE`:

```sql
CREATE TABLE users (
    id int64,
    name string,
    city string,
    age int64,
    INDEX (city),
    INDEX (city, age),
    PRIMARY KEY (id)
);
```

This chapter does not add a separate `CREATE INDEX` statement.

The parser's create-table representation changes from:

```go
type StmtCreatTable struct {
	table string
	cols  []Column
	pkey  []string
}
```

to:

```go
type StmtCreatTable struct {
	table   string
	cols    []Column
	pkey    []string
	indices [][]string
}
```

`pkey` remains separate while parsing because SQL distinguishes `PRIMARY KEY`
from ordinary `INDEX` declarations.

`parseCreateTableItem()` recognizes an index and collects its column names:

```go
if p.tryKeyword("INDEX") {
	index := []string{}
	err := p.parseCommaList(func() error {
		return p.parseNameItem(&index)
	})
	if err == nil {
		out.indices = append(out.indices, index)
	}
	return err
}
```

For the SQL above, the parser produces the conceptual result:

```text
pkey:
  [id]

indices:
  [city]
  [city, age]
```

At this point they are names, not numeric column positions.

---

## 12. One Schema Representation for Every Index

The old schema has a special primary-key field:

```go
type Schema struct {
	Table string
	Cols  []Column
	PKey  []int
}
```

Chapter 0801 replaces it with:

```go
type Schema struct {
	Table   string
	Cols    []Column
	Indices [][]int
}
```

The convention is:

```text
Indices[0] = primary key
Indices[1] = first secondary index
Indices[2] = second secondary index
```

For the `users` example, column positions are:

```text
0: id
1: name
2: city
3: age
```

The primary key begins as:

```text
Indices[0] = [0]
```

The secondary definitions begin as:

```text
city:      [2]
city, age: [2, 3]
```

After appending the primary key, the final schema is:

```text
Indices[0] = [0]       primary key
Indices[1] = [2, 0]    city + primary key
Indices[2] = [2, 3, 0] city + age + primary key
```

---

## 13. Converting Parser Output to `Schema`

`execCreateTable()` combines the primary and secondary definitions:

```go
for i, names := range append([][]string{stmt.pkey}, stmt.indices...) {
	index, err := lookupColumns(stmt.cols, names)
	if err != nil {
		return err
	}
	if i > 0 {
		index = addPKeyToIndex(index, schema.Indices[0])
	}
	schema.Indices = append(schema.Indices, index)
}
```

The expression:

```go
append([][]string{stmt.pkey}, stmt.indices...)
```

creates this processing order:

```text
primary key first
secondary index 1 second
secondary index 2 third
...
```

That order guarantees that `schema.Indices[0]` exists before it is appended to
secondary indexes.

`lookupColumns()` converts names to column positions:

```text
[city, age] -> [2, 3]
```

If a named column does not exist, table creation returns an error.

The completed schema is serialized as JSON and stored under:

```text
@schema_<table name>
```

For the users table:

```text
@schema_users -> serialized Schema
```

---

## 14. Appending Primary-Key Columns Correctly

`addPKeyToIndex()` appends any primary-key column not already present:

```go
func addPKeyToIndex(index []int, pkey []int) []int {
	for _, idx := range pkey {
		if !slices.Contains(index, idx) {
			index = append(index, idx)
		}
	}
	return index
}
```

Suppose:

```text
columns:
0 = account
1 = sequence
2 = status
3 = created_at

primary key:
(account, sequence) -> [0, 1]
```

For:

```sql
INDEX (status)
```

the result is:

```text
[2] + [0, 1] -> [2, 0, 1]
```

For:

```sql
INDEX (status, account)
```

`account` is already present, so only `sequence` is appended:

```text
[2, 0] + missing parts of [0, 1]
    |
    v
[2, 0, 1]
```

Avoiding duplicate columns keeps the key definition unambiguous.

---

## 15. Complete Chapter Example

Consider:

```sql
CREATE TABLE messages (
    id int64,
    sender string,
    recipient string,
    sent_at int64,
    INDEX (sender),
    INDEX (recipient, sent_at),
    PRIMARY KEY (id)
);
```

### Parser output

```text
table = messages

cols:
  0: id
  1: sender
  2: recipient
  3: sent_at

pkey:
  [id]

indices:
  [sender]
  [recipient, sent_at]
```

### Convert names to positions

```text
primary:
  [id] -> [0]

secondary 1:
  [sender] -> [1]

secondary 2:
  [recipient, sent_at] -> [2, 3]
```

### Append the primary key

```text
Indices[0] = [0]
Indices[1] = [1, 0]
Indices[2] = [2, 3, 0]
```

The schema now says that future KV keys should conceptually represent:

```text
primary:
(id)

sender index:
(sender, id)

recipient/time index:
(recipient, sent_at, id)
```

For this row:

```text
id        = 42
sender    = alice
recipient = bob
sent_at   = 1700
```

the future index entries will conceptually be:

```text
primary entry:
(messages, primary, 42) -> complete non-primary row data

sender entry:
(messages, sender-index, alice, 42) -> empty

recipient/time entry:
(messages, recipient-time-index, bob, 1700, 42) -> empty
```

0801 records the definitions needed to produce those entries. It does not
write the secondary entries yet.

---

## 16. Refactoring Existing Primary-Key Code

Because `Schema.PKey` no longer exists, every existing primary-key operation
must use:

```go
schema.Indices[0]
```

The affected behavior includes:

- encoding a row's primary KV key;
- encoding primary-key range prefixes;
- excluding primary-key columns from the stored value;
- decoding primary-key columns from a KV key;
- seeking by primary key;
- recognizing primary-key query conditions; and
- preventing updates to primary-key columns.

For example, `Row.EncodeKey()` changes its loop from:

```go
for _, idx := range schema.PKey {
```

to:

```go
for _, idx := range schema.Indices[0] {
```

At this stage, existing reads and writes still operate through only
`Indices[0]`. The other definitions are stored but not used.

---

## 17. What 0801 Does Not Implement

Chapter 0801 does not yet:

- add an index number to encoded KV keys;
- create secondary entries during `INSERT`;
- remove secondary entries during `DELETE`;
- replace secondary entries during `UPDATE`;
- select an index for a query;
- follow a secondary entry back to its primary row; or
- make multi-key row/index updates atomic.

Those responsibilities are introduced gradually:

```text
0801: parse and store index definitions
  |
  v
0802: maintain secondary-index KV entries
  |
  v
0803: use indexes for range queries
  |
  v
0804: group multi-key changes in transactions
```

This separation is useful: 0801 changes the data model first without changing
the physical write and query behavior at the same time.

---

## Implementation Checklist

- Add `indices [][]string` to `StmtCreatTable`.
- Parse `INDEX (...)` items inside `CREATE TABLE`.
- Allow more than one secondary index declaration.
- Replace `Schema.PKey` with `Schema.Indices [][]int`.
- Store the primary key in `Schema.Indices[0]`.
- Convert secondary-index column names to numeric positions.
- Append missing primary-key columns to each secondary index.
- Avoid adding a primary-key column twice.
- Store secondary indexes after the primary index.
- Update row encoding and decoding to use `Indices[0]`.
- Update table seek and range matching to use `Indices[0]`.
- Continue rejecting updates to primary-key columns.
- Update schemas constructed directly in tests.
- Test the final `Schema.Indices` ordering and contents.
- Do not create secondary KV entries yet.

---

## Review Questions

1. What is the difference between a primary index and a secondary index?
2. Why are secondary-index values often not unique?
3. What are the two common ways to represent duplicate secondary keys?
4. Why does this project append the primary key to a secondary index?
5. What is the difference between a heap file and a clustered index?
6. Why does a nonclustered index require an extra lookup?
7. When does an index cover a query?
8. Why do extra indexes make writes more expensive?
9. Which searches are efficient with an index on `(last_name, first_name)`?
10. Why is `(first_name)` alone not an efficient seek through that index?
11. Why does a concatenated `(latitude, longitude)` index not fully solve a
    two-dimensional range query?
12. Why is the primary key stored at `Schema.Indices[0]`?
13. Why must `execCreateTable()` process the primary key before secondary
    indexes?
14. Why does `addPKeyToIndex()` check for existing columns before appending?
15. What index functionality is deliberately postponed until 0802 and 0803?

---

## Quick Review

```text
Primary index
-------------
unique row identity -> row data

Secondary index
---------------
search columns + primary key -> path to primary row

Schema representation
---------------------
Indices[0] -> primary key
Indices[1] -> secondary index 1 + missing primary-key columns
Indices[2] -> secondary index 2 + missing primary-key columns

Chapter boundary
----------------
0801 defines indexes
0802 maintains indexes
0803 queries indexes
0804 makes multi-key changes transactional
```

The central rule to remember is:

> A secondary index stores another searchable ordering of the same rows, and
> appending the primary key makes each entry unique while preserving a path
> back to the complete row.

## Source

Based on Chapter 0801, “Indexes and KV,” printed pages 82–83 of
`/home/morgankim/Documents/ebooks/db_in_45_steps_go.pdf`, cross-checked against
`db_solution/0801`. The conceptual sections also incorporate the supplied
“Other indexing structures” excerpt from Chapter 3, “Storage and Retrieval,”
and the follow-up discussion in the shared ChatGPT conversation:
`https://chatgpt.com/share/6aa23641-31c8-83e8-882a-e6015a68b898`.
