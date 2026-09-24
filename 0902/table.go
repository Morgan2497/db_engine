package db0902

import (
	"bytes"
	"encoding/json"
	"errors"
	"slices"
)

type DBTX struct {
	kv     *KVTX
	tables map[string]Schema
}

type DB struct {
	KV KV
}

type SQLResult struct {
	Updated int
	Header  []interface{}
	Values  []Row
}

type RowIterator struct {
	tx      *DBTX
	schema  *Schema
	indexNo int
	iter    *RangedKVIter
	valid   bool // decode result (err != ErrOutofRange), a cached boolean telling if the last move was successful.
	row     Row  // decode result, a cached Go struct (row) holding the fully decoded row data.
}

type ExprOp uint8

type ExprTuple struct {
	kids []interface{}
}

type RangeReq struct {
	StartCmp ExprOp // <= >= < >
	StopCmp  ExprOp
	Start    []Cell
	Stop     []Cell
	IndexNo  int
}

type ExprBinOp struct {
	op    ExprOp
	left  interface{}
	right interface{}
}

type ExprUnOp struct {
	op  ExprOp
	kid interface{}
}

const (
	OP_EQ ExprOp = 10 // =
	OP_NE ExprOp = 11 // != or <>
	OP_LE ExprOp = 12 // <=
	OP_GE ExprOp = 13 // >=
	OP_LT ExprOp = 14 // <
	OP_GT ExprOp = 15 // >

	OP_ADD ExprOp = 16 // +
	OP_SUB ExprOp = 17 // -
	OP_MUL ExprOp = 18 // *
	OP_DIV ExprOp = 19 // /

	OP_AND ExprOp = 20 // AND
	OP_OR  ExprOp = 21 // OR

	OP_NOT ExprOp = 30 // NOT
	OP_NEG ExprOp = 31 // unary -
)

func (db *DB) NewTX() *DBTX {
	return &DBTX{
		kv:     db.KV.NewTX(),
		tables: make(map[string]Schema),
	}
}

func (tx *DBTX) Abort() {
	tx.kv.Abort()
}

func (tx *DBTX) Commit() error {
	return tx.kv.Commit()
}

func (tx *DBTX) NewTX() *DBTX {
	return &DBTX{
		kv:     tx.kv.NewTX(),
		tables: make(map[string]Schema),
	}
}

func suffixPositive(op ExprOp) bool {
	switch op {
	case OP_LE, OP_GT:
		return true

	case OP_LT, OP_GE:
		return false

	default:
		panic("invalid comparison operator")
	}
}

func isDescending(op ExprOp) bool {
	switch op {
	case OP_LE, OP_LT:
		return true

	case OP_GE, OP_GT:
		return false

	default:
		panic("invalid comparison operator")
	}
}

// Is iteration finished? (Direct boolean read in RAM)
func (iter *RowIterator) Valid() bool { return iter.valid }

// Current row accessor (Direct struct read in RAM)
func (iter *RowIterator) Row() Row {
	check(iter.valid)
	return iter.row
}

func (iter *RowIterator) Next() (err error) {
	if err = iter.iter.Next(); err != nil {
		return err
	}
	iter.valid, err = iter.decodeKVIter()
	return err
}

// Helper (translator) that sits between the physical storage engine and the relational row struct.
/*
It does three things.
1. Check if the raw iterator is valid.
2. Decode and verify the key.
3. Decode the value.
*/
func (iter *RowIterator) decodeKVIter() (bool, error) {
	if !iter.iter.Valid() {
		return false, nil
	}

	if err := iter.row.DecodeKey(
		iter.schema,
		iter.indexNo,
		iter.iter.Key(),
	); err != nil {
		if errors.Is(err, ErrOutOfRange) {
			return false, nil
		}
		return false, err
	}

	if iter.indexNo > 0 {
		ok, err := iter.tx.Select(iter.schema, iter.row)
		if err != nil {
			return false, err
		}
		if !ok {
			return false, errors.New("inconsistent index")
		}
	} else {
		if err := iter.row.DecodeVal(
			iter.schema,
			iter.iter.Val(),
		); err != nil {
			return false, err
		}
	}
	return true, nil
}

// Seek preserves the old open-ended relational scan API by wrapping a bounded
// ascending KV range whose stop key is the end of this table's namespace.

func (tx *DBTX) Seek(schema *Schema, row Row) (*RowIterator, error) {
	start := make([]Cell, len(schema.Indices[0]))
	for i, col := range schema.Indices[0] {
		check(row[col].Type == schema.Cols[col].Type)
		start[i] = row[col]
	}
	return tx.Range(schema, &RangeReq{
		StartCmp: OP_GE,
		Start:    start,
		StopCmp:  OP_LE,
		Stop:     nil,
		IndexNo:  0,
	})
}
func (db *DB) Seek(schema *Schema, row Row) (*RowIterator, error) {
	tx := db.NewTX()
	return tx.Seek(schema, row)
}

func (db *DB) Open() error {
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
func (tx *DBTX) GetSchema(table string) (Schema, error) {
	// 1. Attempt to get the schema if exists in map (RAM cache first).
	schema, ok := tx.tables[table]

	if !ok {
		// 1. Attempt durable read: fallback to the physical KV engine.
		val, ok, err := tx.kv.Get([]byte("@schema_" + table))
		if err == nil && ok {
			err = json.Unmarshal(val, &schema)
		}
		if err != nil {
			return Schema{}, err
		}
		if !ok {
			return Schema{}, errors.New("table is not found")
		}
		tx.tables[table] = schema
	}
	return schema, nil
}

func (db *DB) GetSchema(table string) (Schema, error) {
	tx := db.NewTX()
	defer tx.Abort()
	return tx.GetSchema(table)
}

func addPKeyToIndex(index []int, pkey []int) []int {
	for _, idx := range pkey {
		if !slices.Contains(index, idx) {
			index = append(index, idx)
		}
	}
	return index
}

// It translates requested column names into their physical integer indices based on the schema definition.
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
	if len(schema.Indices[0]) != len(pkey) {
		return nil, errors.New("not primary key")
	}

	row := schema.NewRow()
	for _, idx1 := range schema.Indices[0] {
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

func matchPKey(schema *Schema, cond interface{}) (Row, error) {
	keys, ok := matchAllEq(cond, nil)
	if !ok {
		return nil, errors.New("unimplemented WHERE")
	}

	return makePKey(schema, keys)
}

func extractPKey(schema *Schema, keys []NamedCell) ([]Cell, bool) {
	if len(schema.Indices[0]) != len(keys) {
		return nil, false
	}

	pkey := make([]Cell, len(schema.Indices[0]))
	for i, schemaIndex := range schema.Indices[0] {
		column := schema.Cols[schemaIndex]

		keyIndex := slices.IndexFunc(keys, func(key NamedCell) bool {
			return key.column == column.Name &&
				key.value.Type == column.Type
		})

		if keyIndex < 0 {
			return nil, false
		}

		pkey[i] = keys[keyIndex].value
	}

	return pkey, true
}

// asNameList converts either a single column name or a tuple of column names
// into one consistent representation.
func asNameList(expr interface{}) ([]string, bool) {
	switch value := expr.(type) {
	case string:
		return []string{value}, true
	case *ExprTuple:
		names := make([]string, len(value.kids))
		for i, child := range value.kids {
			name, ok := child.(string)
			if !ok {
				return nil, false
			}
			names[i] = name
		}
		return names, true
	default:
		return nil, false
	}
}

// asCellList converts either a single literal or a tuple of literals into a
// primary-key prefix that DB.Range can encode.
func asCellList(expr interface{}) ([]Cell, bool) {
	switch value := expr.(type) {
	case *Cell:
		return []Cell{*value}, true
	case *ExprTuple:
		cells := make([]Cell, len(value.kids))
		for i, child := range value.kids {
			cell, ok := child.(*Cell)
			if !ok {
				return nil, false
			}
			cells[i] = *cell
		}
		return cells, true
	default:
		return nil, false
	}
}

func reverseComparison(op ExprOp) ExprOp {
	switch op {
	case OP_LT:
		return OP_GT
	case OP_LE:
		return OP_GE
	case OP_GT:
		return OP_LT
	case OP_GE:
		return OP_LE
	default:
		return op
	}
}

// matchCmp recognizes comparisons such as id > 20, 20 < id, and
// (tenant, id) >= ("acme", 10).
func matchCmp(cond interface{}) (ExprOp, []string, []Cell, bool) {
	comparison, ok := cond.(*ExprBinOp)
	if !ok {
		return 0, nil, nil, false
	}

	switch comparison.op {
	case OP_LT, OP_LE, OP_GT, OP_GE:
	default:
		return 0, nil, nil, false
	}

	op := comparison.op
	names, namesOK := asNameList(comparison.left)
	cells, cellsOK := asCellList(comparison.right)
	if namesOK && cellsOK {
		return op, names, cells, true
	}

	// If the SQL is 20 < id, swap the operands and reverse the operator so the
	// normalized result is id > 20.
	names, namesOK = asNameList(comparison.right)
	cells, cellsOK = asCellList(comparison.left)
	if !namesOK || !cellsOK {
		return 0, nil, nil, false
	}
	return reverseComparison(op), names, cells, true
}

func isPKeyPrefix(schema *Schema, indexNo int, names []string, cells []Cell) bool {
	if len(names) == 0 || len(names) != len(cells) || len(names) > len(schema.Indices[indexNo]) {
		return false
	}

	for i, name := range names {
		column := schema.Cols[schema.Indices[indexNo][i]]
		if name != column.Name || cells[i].Type != column.Type {
			return false
		}
	}
	return true
}

func matchRangeByIndex(schema *Schema, indexNo int, cond interface{}) (*RangeReq, bool) {
	condition, ok := cond.(*ExprBinOp)
	if !ok {
		return nil, false
	}

	if condition.op == OP_AND {
		leftRange, leftOK := matchRangeByIndex(schema, indexNo, condition.left)
		if !leftOK {
			return nil, false
		}

		rightRange, rightOK := matchRangeByIndex(schema, indexNo, condition.right)
		if !rightOK {
			return nil, false
		}

		// This chapter supports one lower boundary and one upper boundary.
		if leftRange.Start != nil && rightRange.Start != nil {
			return nil, false
		}

		if leftRange.Stop != nil && rightRange.Stop != nil {
			return nil, false
		}

		combined := &RangeReq{
			IndexNo: indexNo,
		}

		if leftRange.Start != nil {
			combined.StartCmp = leftRange.StartCmp
			combined.Start = leftRange.Start
		} else {
			combined.StartCmp = rightRange.StartCmp
			combined.Start = rightRange.Start
		}

		if leftRange.Stop != nil {
			combined.StopCmp = leftRange.StopCmp
			combined.Stop = leftRange.Stop
		} else {
			combined.StopCmp = rightRange.StopCmp
			combined.Stop = rightRange.Stop
		}
		return combined, true
	}

	op, names, cells, ok := matchCmp(cond)
	if !ok || !isPKeyPrefix(schema, indexNo, names, cells) {
		return nil, false
	}

	switch op {
	case OP_GT, OP_GE:
		return &RangeReq{
			StartCmp: op,
			StopCmp:  OP_LE,
			Start:    cells,
			Stop:     nil,
			IndexNo:  indexNo,
		}, true
	case OP_LT, OP_LE:
		return &RangeReq{
			StartCmp: op,
			StopCmp:  OP_GE,
			Start:    cells,
			Stop:     nil,
			IndexNo:  indexNo,
		}, true
	}
	return nil, false
}

func matchRange(schema *Schema, cond interface{}) (*RangeReq, bool) {
	for indexNo := range schema.Indices {
		req, ok := matchRangeByIndex(schema, indexNo, cond)
		if ok {
			return req, true
		}
	}
	return nil, false
}

func makeRange(schema *Schema, cond interface{}) (*RangeReq, error) {
	if keys, ok := matchAllEq(cond, nil); ok {
		if pkey, ok := extractPKey(schema, keys); ok {
			return &RangeReq{
				StartCmp: OP_GE,
				StopCmp:  OP_LE,
				Start:    pkey,
				Stop:     pkey,
			}, nil
		}
	}
	if req, ok := matchRange(schema, cond); ok {
		return req, nil
	}
	return nil, errors.New("unimplemented WHERE")
}
func matchAllEq(cond interface{}, out []NamedCell) ([]NamedCell, bool) {
	node, ok := cond.(*ExprBinOp)
	if !ok {
		return nil, false
	}

	if node.op == OP_AND {
		var matched bool

		out, matched = matchAllEq(node.left, out)
		if !matched {
			return nil, false
		}
		return matchAllEq(node.right, out)
	}

	if node.op != OP_EQ {
		return nil, false
	}

	left, right := node.left, node.right
	column, columnOK := left.(string)
	if !columnOK {
		left, right = right, left
		column, columnOK = left.(string)
	}
	value, valueOK := right.(*Cell)
	if !columnOK || !valueOK {
		return nil, false
	}

	out = append(out, NamedCell{
		column: column,
		value:  *value,
	})
	return out, true
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
		if idx < 0 || slices.Contains(schema.Indices[0], idx) {
			return errors.New("cannot update column")
		}

		// Safely apply the mutation to the row
		out[idx] = expr.value
	}
	return nil
}

// DDL: Data Definition Language.
// It defines the physical rules of the database.
func (tx *DBTX) execCreateTable(stmt *StmtCreateTable) (err error) {
	// 0. Check if already exists.
	if _, err := tx.GetSchema(stmt.table); err == nil {
		return errors.New("duplicate table name")
	}

	// 1. The struct translation.
	schema := Schema{
		Table: stmt.table,
		Cols:  stmt.cols,
	}

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
	if len(schema.Indices) > 256 {
		return errors.New("too many indices")
	}

	// 2. The JSON Serialization.
	// Convert the Go struct into raw bytes for the KV store.
	val, err := json.Marshal(schema)
	if err != nil {
		return err
	}

	// 3. Durable Write.
	// Prefix the key so the storage engine knows this is metadata, not user data.
	_, err = tx.kv.Set([]byte("@schema_"+schema.Table), val)
	if err != nil {
		return err
	}

	// 4. The Cache Population.
	// Make the schema instantly available in RAM for future queries.
	tx.tables[schema.Table] = schema

	return nil
}

// DQL (Data Query Language) pipeline.
// Its job is to act as the ultimate translator between the user's abstract SQL string
// and the physical hardware of the storage engine.
func (tx *DBTX) execCond(schema *Schema, cond interface{}) (*RowIterator, error) {
	req, err := makeRange(schema, cond)
	if err != nil {
		return nil, err
	}
	return tx.Range(schema, req)
}

func (tx *DBTX) execSelect(stmt *StmtSelect) (output []Row, err error) {
	schema, err := tx.GetSchema(stmt.table)
	if err != nil {
		return nil, err
	}

	iter, err := tx.execCond(&schema, stmt.cond)
	if err != nil {
		return nil, err
	}

	for ; err == nil && iter.Valid(); err = iter.Next() {
		row := iter.Row()
		computed := make(Row, len(stmt.cols))
		for i, expr := range stmt.cols {
			cell, evalErr := evalExpr(&schema, row, expr)
			if evalErr != nil {
				return nil, evalErr
			}
			computed[i] = *cell
		}
		output = append(output, computed)
	}

	if err != nil {
		return nil, err
	}
	return output, nil
}

// DML: Data Manipulation Language (DML): the memory translation.
func (tx *DBTX) execInsert(stmt *StmtInsert) (count int, err error) {
	// 1. Fetch the metadata
	schema, err := tx.GetSchema(stmt.table)
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
	inserted, err := tx.Insert(&schema, stmt.value)
	if err != nil {
		return 0, err
	}

	if inserted {
		return 1, nil
	}
	return 0, nil
}

func (tx *DBTX) execUpdate(stmt *StmtUpdate) (count int, err error) {
	schema, err := tx.GetSchema(stmt.table)
	if err != nil {
		return 0, err
	}

	iter, err := tx.execCond(&schema, stmt.cond)
	if err != nil {
		return 0, err
	}

	oldRows := []Row{}

	for ; err == nil && iter.Valid(); err = iter.Next() {
		oldRows = append(oldRows, slices.Clone(iter.Row()))
	}
	if err != nil {
		return 0, err
	}

	for _, row := range oldRows {
		updates := make([]NamedCell, len(stmt.value))
		for i, assign := range stmt.value {
			cell, err := evalExpr(&schema, row, assign.expr)
			if err != nil {
				return 0, err
			}
			updates[i] = NamedCell{column: assign.column, value: *cell}
		}
		if err = fillNonPKey(&schema, updates, row); err != nil {
			return 0, err
		}

		updated, updateErr := tx.Update(&schema, row)
		if updateErr != nil {
			return 0, updateErr
		}
		if updated {
			count++
		}
	}
	return count, nil
}

func (tx *DBTX) execDelete(stmt *StmtDelete) (count int, err error) {
	schema, err := tx.GetSchema(stmt.table)
	if err != nil {
		return 0, err
	}

	iter, err := tx.execCond(&schema, stmt.cond)
	if err != nil {
		return 0, err
	}

	// RowIterator holds a view of the KV slices. Deleting from those slices
	// during iteration would shift elements and skip rows, so collect first.
	rows := []Row{}
	for ; err == nil && iter.Valid(); err = iter.Next() {
		rows = append(rows, slices.Clone(iter.Row()))
	}
	if err != nil {
		return 0, err
	}

	for _, row := range rows {
		deleted, deleteErr := tx.Delete(&schema, row)
		if deleteErr != nil {
			return 0, deleteErr
		}
		if deleted {
			count++
		}
	}

	return count, nil
}

func (db *DB) Select(schema *Schema, row Row) (ok bool, err error) {
	tx := db.NewTX()
	defer tx.Abort()

	return tx.Select(schema, row)
}

func (tx *DBTX) Select(schema *Schema, row Row) (ok bool, err error) {
	// 1. We just encode the key.
	key := row.EncodeKey(schema, 0)

	// 2. Query the underlying storage engine.
	val, ok, err := tx.kv.Get(key)

	if err != nil || !ok {
		return ok, err
	}

	// 3. The key exists. Decode the raw byte value back into the row's columns.
	if err = row.DecodeVal(schema, val); err != nil {
		return false, err
	}
	return true, nil
}

func (tx *DBTX) update(schema *Schema, row Row, mode UpdateMode) (updated bool, err error) {
	key := row.EncodeKey(schema, 0)
	val := row.EncodeVal(schema)

	oldVal, exists, err := tx.kv.Get(key)
	if err != nil {
		return false, err
	}

	switch mode {
	case ModeUpsert:
		updated = !exists || !bytes.Equal(oldVal, val)
	case ModeInsert:
		updated = !exists
	case ModeUpdate:
		updated = exists && !bytes.Equal(oldVal, val)
	default:
		panic("unreachable")
	}

	if !updated {
		return false, nil
	}

	if exists {
		oldRow := slices.Clone(row)
		if err = oldRow.DecodeVal(schema, oldVal); err != nil {
			return false, err
		}
		if _, err = tx.delete(schema, oldRow); err != nil {
			return false, err
		}
	}

	for i := 0; i < len(schema.Indices) && err == nil; i++ {
		if i > 0 {
			key = row.EncodeKey(schema, i)
			val = nil
		}
		updated, err = tx.kv.SetEx(key, val, ModeInsert)
		if err == nil && !updated {
			panic("impossible")
		}
	}
	return updated, err
}

func (tx *DBTX) Insert(schema *Schema, row Row) (updated bool, err error) {
	tx = tx.NewTX()
	updated, err = tx.update(schema, row, ModeInsert)
	return abortOrCommit(tx, updated, err)
}

func (db *DB) Insert(schema *Schema, row Row) (updated bool, err error) {
	tx := db.NewTX()
	updated, err = tx.update(schema, row, ModeInsert)
	return abortOrCommit(tx, updated, err)
}

func (tx *DBTX) Upsert(schema *Schema, row Row) (updated bool, err error) {
	tx = tx.NewTX()
	updated, err = tx.update(schema, row, ModeUpsert)
	return abortOrCommit(tx, updated, err)
}

func (tx *DBTX) Update(schema *Schema, row Row) (updated bool, err error) {
	tx = tx.NewTX()
	updated, err = tx.update(schema, row, ModeUpdate)
	return abortOrCommit(tx, updated, err)
}

func (db *DB) Upsert(schema *Schema, row Row) (updated bool, err error) {
	tx := db.NewTX()
	updated, err = tx.update(schema, row, ModeUpsert)
	return abortOrCommit(tx, updated, err)
}

func (db *DB) Update(schema *Schema, row Row) (updated bool, err error) {
	tx := db.NewTX()
	updated, err = tx.update(schema, row, ModeUpdate)
	return abortOrCommit(tx, updated, err)
}

func (tx *DBTX) execStmt(stmt interface{}) (r SQLResult, err error) {
	switch ptr := stmt.(type) {
	case *StmtCreateTable:
		err = tx.execCreateTable(ptr)

	case *StmtSelect:
		r.Header = ptr.cols
		r.Values, err = tx.execSelect(ptr)

	case *StmtInsert:
		r.Updated, err = tx.execInsert(ptr)

	case *StmtUpdate:
		r.Updated, err = tx.execUpdate(ptr)

	case *StmtDelete:
		r.Updated, err = tx.execDelete(ptr)

	default:
		panic("unreachable")
	}
	return r, err
}

func (tx *DBTX) ExecStmt(stmt interface{}) (r SQLResult, err error) {
	tx = tx.NewTX()
	r, err = tx.execStmt(stmt)
	if _, err = abortOrCommit(tx, true, err); err != nil {
		return SQLResult{}, err
	}
	return r, nil
}

func (db *DB) ExecStmt(stmt interface{}) (r SQLResult, err error) {
	tx := db.NewTX()
	r, err = tx.execStmt(stmt)
	if _, err = abortOrCommit(tx, true, err); err != nil {
		return SQLResult{}, err
	}
	return r, nil
}

func (tx *DBTX) delete(schema *Schema, row Row) (deleted bool, err error) {
	for i := 0; i < len(schema.Indices) && err == nil; i++ {
		key := row.EncodeKey(schema, i)

		deleted, err = tx.kv.Del(key)

		if err == nil && !deleted {
			if i != 0 {
				return false, errors.New("inconsistent index")
			}
			break
		}
	}
	return deleted, err
}

func (tx *DBTX) Delete(schema *Schema, row Row) (deleted bool, err error) {
	tx = tx.NewTX()

	deleted, err = tx.delete(schema, row)

	return abortOrCommit(tx, deleted, err)
}

func (db *DB) Delete(schema *Schema, row Row) (deleted bool, err error) {
	tx := db.NewTX()

	deleted, err = tx.delete(schema, row)

	return abortOrCommit(tx, deleted, err)
}

func (tx *DBTX) Range(schema *Schema, req *RangeReq) (*RowIterator, error) {
	check(isDescending(req.StartCmp) != isDescending(req.StopCmp))

	start := EncodeKeyPrefix(
		schema,
		req.IndexNo,
		req.Start,
		suffixPositive(req.StartCmp),
	)

	stop := EncodeKeyPrefix(
		schema,
		req.IndexNo,
		req.Stop,
		suffixPositive(req.StopCmp),
	)

	desc := isDescending(req.StartCmp)

	kvIter, err := tx.kv.Range(start, stop, desc)
	if err != nil {
		return nil, err
	}

	iter := &RowIterator{
		tx:      tx,
		schema:  schema,
		indexNo: req.IndexNo,
		iter:    kvIter,
		row:     schema.NewRow(),
	}

	iter.valid, err = iter.decodeKVIter()
	if err != nil {
		return nil, err
	}

	return iter, nil
}

func (db *DB) Range(schema *Schema, req *RangeReq) (*RowIterator, error) {
	tx := db.NewTX()
	return tx.Range(schema, req)
}
