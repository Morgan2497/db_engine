package kv

type DB struct {
	KV KV
}

func (db *DB) Open() error  { return db.KV.Open() }
func (db *DB) Close() error { return db.KV.Close() }

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

func (db *DB) Delete(schema *Schema, row Row) (deleted bool, err error) {
	key := row.EncodeKey(schema)
	return db.KV.Del(key)
}
