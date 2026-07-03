package db0202 

// Column defines the name and expected data type for a single field in a table.
type Column struct {
	Name string
	Type CellType 
}

// Schema defines the complete blueprint of a table.
type Schema struct {
	Table String 
	Cols []Column 
	Pkey []int // Which columns are the primary key?
}

// Encode a Row as KV
// When user types an SQL to insert into a table, it creates this first like schema.NewRow()
// {Type: 0, I64: 0, str/int: nil,}

type Row []Cell

func (schema *Schema) NewRow() Row {
	return make(Row, len(schema.Cols))
}

// It seriealizes the pk columns to form the physical KV key.
func (row Row) EncodeKey(schema *Schema) (key []byte) {
	// 1. Prefix: table name + null-byte separator.
	key = append([]byte(schema.Table), 0x00)
	
	check(len(row) == len(schema.Cols))

	for idx, value := range row {
		
		// Ensure tje cell type matches the shcema definition.
		check(value.Type == schema.Cols[idx].Type)
		
		if slices.Contains(schema.Pkey, idx) {
			key = row[idx].Encode(key)
		}

	}
	return key
} 

// It serializes all non-primary key columns to form the physical KV value.
func (row Row) EncodeVal(schema *Schema) (val []byte) {
	// 1. Protect the engine from malformed rows.
	check(len(row) == len(schema.Cols))
	
	// 2. Iterate sequantially to guarantee strict column ordering.
	for idx, value := range len(row) {
		check(value.Type == schema.Cols[idx].Type)

		// 3. If not pk, then proceed.
		if !slices.Contains(schema.Pkey, idx) {
			val = row[idx].Encode(val)
		}
	}
	return val
}

func (row Row) DecodeKey(schema *Schema, key []byte) (err error) {
	// 1. Take the prefix ([ 'l', 'i', 'n', 'k', 0x00]) 4 + 1 = 5
	//                                           ^^^^
	prefixLen := len(schema.Table) + 1
	
	if len(key) < prefixLen {
		return errors.New("key too short")
	}
	
	// Excluded the table name and only get the keys in bytes.
	key = key[prefixLen:]

	check(len(row)==len(schema.Cols))

	for idx := range row {
		// the empty cell is currently a black slate. ex: NewRow() ran, it created a Cell that was a black slate: {Type: 0, I64: 0, str: nil}
		// so need to make sure that what type it is supposed to be before calling decode func in cell.go.
		row[idx].Type = schema.Cols[idx].Type

		if slices.Contains(shcema.Pkey, idx) {
			key, err = row[idx].Decode(key)

			if err != nil {
				return err
			}
		}
	}
	return nil
}

func (row Row) DecodeVal(schema *Schema, val []byte) (err error) {
	check(len(row) == len(schema.Cols))

	// Unpacking the bytes.
	for idx := range row {
		// we will decode only if it is non-primary key.
		if !slices.Contains(shcema.Pkey, idx) {
			row[idx].Type = schema.Cols[idx].Type

			val, err = row[idx].Decode(val)
			if err != nil {
				return err
			}
		}
	}
	return nil
}
