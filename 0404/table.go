package kv

import (
	"encoding/json"
	"errors"
	"slices"
)

type DB struct {
	KV KV
	tables map[string]Schema
}

type SQLResult struct {
	Updated int 
	Header []string 
	Values []Row
}

type RowIterator struct {
	schema *Schema
	iter *KVIterator
	valid bool // decode result (err != ErrOutofRange), a cached boolean telling if the last move was successful.
	row Row // decode result, a cached Go struct (row) holding the fully decoded row data.
}

// Is iteration finished? (Direct boolean read in RAM)
func (iter *RowIterator) Valid() bool {return iter.valid}

// Current row accessor (Direct struct read in RAM)
func (iter *RowIterator) Row() Row {return iter.row}

func (iter *RowIterator) Next() (err error) {
	if err = iter.iter.Next(); err != nil {
		return err
	}
	iter.valid, err = decodeKVIter(iter.schema, iter.iter, iter.row)
	return err
}

// Helper (translator) that sits between the physical storage engine and the relational row struct.
/*
It does three things.
1. Check if the raw iterator is valid.
2. Decode and verify the key.
3. Decode the value.
*/
func decodeKVIter(schema *Schema, iter *KVIterator, row Row) (bool, error) {
	// 1. Check if the raw KV cursor is even active.
	if !iter.Valid() {
		return false, nil
	}

	// 2. Extract and decode the raw key, checking table boundaries.
	key := iter.Key()
	if err := row.DecodeKey(schema, key); err != nil {
		if errors.Is(err, ErrOutOfRange) {
			return false, nil // hits table boundary.
		}
		return  false, err // error occured.
	}

	// 3. Extract and decode the value payload for remaining columns.
	val := iter.Val()
	if err := row.DecodeVal(schema, val); err != nil {
		return false, err
	}
	return true, nil
}

// the entry point that brdiges relational Row, encodes it in to raw bytes, asks the storage to find the pos.
func (db *DB) Seek(schema *Schema, row Row) (*RowIterator, error) {
	// 1. Translate the target relational row into an order-preserving byte key.
	key := row.EncodeKey(schema)
	
	// 2. Position the physical storage curosr at the first key >= target.
	kvIter, err := db.KV.Seek(key)
	if err != nil {
		return nil, err
	}

	// 3. Initialize our cached state wrapper (RowIterator)
	iter := &RowIterator {
		schema: schema,
		iter: kvIter,
		row: schema.NewRow(),
	}
	
	// 4. Immediately decode and validate the intial pos.
	iter.valid, err = decodeKVIter(schema, kvIter, iter.row)
	if err != nil {
		return nil, err
	}
	return iter, nil
}

func (db *DB) Open() error {
	db.tables = map[string]Schema{}
	return db.KV.Open()
}
func (db *DB) Close() error { return db.KV.Close() }

// The metadata fetcher.
/*
Example skeleton.
myKV := KV{
    mem: map[string][]byte{
        "@schema_users": []byte(`{"Table":"users","Cols":[{"Name":"id","Type":1}],"PKey":[0]}`),
    },
}
*/
func (db *DB) GetSchema(table string) (Schema, error) {
	// 1. Attempt to get the schema if exists in map (RAM cache first).
	schema, ok := db.tables[table]
	
	if !ok {
		// 1. Attempt durable read: fallback to the physical KV engine.
		val, ok, err := db.KV.Get([]byte("@schema_" + table))
		if err == nil && ok {
			err = json.Unmarshal(val, &schema)
		}
		if err != nil {
			return Schema{}, err
		}
		if !ok {
			return Schema{}, errors.New("table is not found")
		}
		db.tables[table] = schema
	}
	return schema, nil
}

// it translates a list of requested string coloumn names into their physical integer indicies based on the schema def.
func lookupColumns(cols []Column, names []string) ([]int, error) {
 indices := make([]int, len(names))

 for i, name := range names {
	found := false
	for j, col := range cols {
		if col.Name == name {
			indices[i] = j 
			found = true
			break
		}
	}
	if !found {
		return nil, errors.New("column not found: " + name)
	}
 }
 return indices, nil
}

func makePKey(schema *Schema, pkey []NamedCell) (Row, error) {
	if len(schema.PKey) != len(pkey) {
		return nil, errors.New("not primary key")
	}

	row := schema.NewRow()
	for _, idx1 := range schema.PKey {
		col := schema.Cols[idx1]
		idx2 := slices.IndexFunc(pkey, func(expr NamedCell) bool {
			return expr.column == col.Name && expr.value.Type == col.Type
		})
		if idx2 < 0 {
			return nil, errors.New("not primary key")
		}
		row[idx1] = pkey[idx2].value
	}
	return row, nil
}

func makeRow(schema *Schema, names []string, vals []Cell) (Row, error) {
	row := schema.NewRow()
	for i, name := range names {
		idx := -1 
		for j, col := range schema.Cols {
			if col.Name == name {
				idx = j 
				break
			}
		}
		if idx < 0 {
			return nil, errors.New("column not found")
		}

		if schema.Cols[idx].Type != vals[i].Type {
			return nil, errors.New("type mismatch")
		}
		row[idx] = vals[i]
	}
	return row, nil
}

func subsetRow(row Row, indices []int) (out Row) {
	for _, idx := range indices {
		out = append(out, row[idx])
	}
	return
}


// fillNonPKey safely updates a row's values while strictly preventing mutations to primary keys.
func fillNonPKey(schema *Schema, updates []NamedCell, out Row) error {
	for _, expr := range updates {
		// Find the physical index for the requested column
		idx := slices.IndexFunc(schema.Cols, func(col Column) bool {
			return col.Name == expr.column && col.Type == expr.value.Type
		})
		if idx < 0 || slices.Contains(schema.PKey, idx) {
			return errors.New("cannot update column")
		}

		// Safely apply the mutation to the row
		out[idx] = expr.value
	}
	return nil
}

// DDL: Data Definition Language. 
// It defines the physical rules of the database.
func (db *DB) execCreateTable(stmt *StmtCreateTable) (err error) {
	// 0. Check if already exists.
	if _, err := db.GetSchema(stmt.table); err == nil {
		return errors.New("duplicate table name")
	}

	// 1. The struct translation.
	schema := Schema{
		Table: stmt.table,
		Cols:  stmt.cols,
	}
	
	// 2. Translate string-based pk into integer indicies.
	if schema.PKey, err = lookupColumns(stmt.cols, stmt.pkey); err != nil {
		return err
	}
	
	// 2. The JSON Serialization.
	// Convert the Go struct into raw bytes for the KV store.
	val, err := json.Marshal(schema)
	if err != nil {
		return err
	}

	// 3. Durable Write.
	// Prefix the key so the storage engine knows this is metadata, not user data.
	_, err = db.KV.Set([]byte("@schema_" + schema.Table), val)
	if err != nil {
		return err
	}

	// 4. The Cache Population.
	// Make the schema instantly available in RAM for future queries.
	db.tables[schema.Table] = schema
	
	return nil
}

// DQL (Data Query Language) pipeline. 
// Its job is to act as the ultimate translator between the user's abstract SQL string
// and the physical hardware of the storage engine. 
func (db *DB) execSelect(stmt *StmtSelect) ([]Row, error) {
	// 1. Fetch the metadata (Checks RAM first, then Disk).
	schema, err := db.GetSchema(stmt.table)
	if err != nil {
		return nil, err
	}

	// 2. Translate the requested return columns into integer indices.
	indices, err := lookupColumns(schema.Cols, stmt.cols)
	if err != nil {
		return nil, err
	}

	// 3. Format the WHERE clause into a physical Row for the disk lookup.
	row, err := makePKey(&schema, stmt.keys)
	if err != nil {
		return nil, err
	}

	// 4. Query the physical Key-Value store.
	if ok, err := db.Select(&schema, row); err != nil || !ok {
		return nil, err
	}

	// 5. Slice off the unrequested columns and return the result.
	row = subsetRow(row, indices)
	return []Row{row}, nil
}

// DML: Data Manipulation Language (DML): the memory translation.
func (db *DB) execInsert(stmt *StmtInsert) (count int, err error) {
	// 1. Fetch the metadata
	schema, err := db.GetSchema(stmt.table)
	if err != nil {
		return 0, err
	}

	// 2. Strict length validation (Boundary enforcement)
	if len(schema.Cols) != len(stmt.value) {
		return 0, errors.New("schema mismatch")
	}

	// 3. Straight-line type validation and row allocation
	for i := range schema.Cols {
		if schema.Cols[i].Type != stmt.value[i].Type {
			return 0, errors.New("schema mismatch")
		}
	}

	// 4. Delegate to KV Store
	inserted, err := db.Insert(&schema, stmt.value)
	if err != nil {
		return 0, err
	}

	if inserted {
		return 1, nil
	}
	return 0, nil
}

func (db *DB) execUpdate(stmt *StmtUpdate) (count int, err error) {
	schema, err := db.GetSchema(stmt.table)
	if err != nil {
		return 0, err
	}

	row, err := makePKey(&schema, stmt.keys)
	if err != nil {
		return 0, err
	}
	if ok, err := db.Select(&schema, row); err != nil || !ok {
		return 0, err
	}

	if err = fillNonPKey(&schema, stmt.value, row); err != nil {
		return 0, err
	}

	updated, err := db.Update(&schema, row)
	if err != nil {
		return 0, err
	}

	if updated {
		return 1, nil
	}
	return 0, nil
}

func (db *DB) execDelete(stmt *StmtDelete) (count int, err error) {
	schema, err := db.GetSchema(stmt.table)
	if err != nil {
		return 0, err
	}

	row, err := makePKey(&schema, stmt.keys)
	if err != nil {
		return 0, err
	}

	deleted, err := db.Delete(&schema, row)
	if err != nil {
		return 0, err
	}
	if deleted {
		return 1, nil
	}

	return 0, nil
}
func (db *DB) Select(schema *Schema, row Row) (ok bool, err error) {
	// 1. We just encode the key.
	key := row.EncodeKey(schema)

	// 2. Query the underlying storage engine.
	val, ok, err := db.KV.Get(key)
	if err != nil || !ok {
		return ok, err
	}

	// 3. The key exists. Decode the raw byte value back into the row's columns.
	if err = row.DecodeVal(schema, val); err != nil {
		return false, err
	}
	return true, nil
}

func (db *DB) Insert(schema *Schema, row Row) (updated bool, err error) {
	key := row.EncodeKey(schema)
	val := row.EncodeVal(schema)
	return db.KV.SetEx(key, val, ModeInsert)
}

func (db *DB) Upsert(schema *Schema, row Row) (updated bool, err error) {
	// 1. Encode the primary key.
	key := row.EncodeKey(schema)

	// 2. Encode the value.
	val := row.EncodeVal(schema)

	// 3. Delegate to the KV store using the unconditional write mode.
	return db.KV.SetEx(key, val, ModeUpsert)
}

func (db *DB) Update(schema *Schema, row Row) (updated bool, err error) {
	// 1. Encode the PK.
	key := row.EncodeKey(schema)

	// 2. Encode the Val(s).
	val := row.EncodeVal(schema)

	return db.KV.SetEx(key, val, ModeUpdate)
}

func (db *DB) ExecStmt(stmt interface{}) (r SQLResult, err error) {
	switch ptr := stmt.(type) {
	case *StmtCreateTable:
		err = db.execCreateTable(ptr)

	case *StmtSelect:
		r.Header = ptr.cols 
		r.Values, err = db.execSelect(ptr)

	case *StmtInsert:
		r.Updated, err = db.execInsert(ptr)

	case *StmtUpdate:
		r.Updated, err = db.execUpdate(ptr)

	case *StmtDelete:
		r.Updated, err = db.execDelete(ptr)

	default:
		panic("unreachable")
	}
	return r, err
}

func (db *DB) Delete(schema *Schema, row Row) (deleted bool, err error) {
	key := row.EncodeKey(schema)
	return db.KV.Del(key)
}
