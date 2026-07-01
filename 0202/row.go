package kv

import (
	"errors"
	"slices"
)

func check(cond bool) {
	if !cond {
		panic("check failed")
	}
}

type Column struct {
	Name string
	Type CellType
}

type Schema struct {
	Table string
	Cols  []Column
	PKey  []int
}

type Row []Cell

func (schema *Schema) NewRow() Row {
	return make(Row, len(schema.Cols))
}

func (row Row) EncodeKey(schema *Schema) (key []byte) {
	key = append([]byte(schema.Table), 0x00)
	check(len(row) == len(schema.Cols))

	for idx, value := range row {
		check(value.Type == schema.Cols[idx].Type)
		if slices.Contains(schema.PKey, idx) {
			key = row[idx].Encode(key)
		}
	}
	return key
}

func (row Row) EncodeVal(schema *Schema) (val []byte) {
	check(len(row) == len(schema.Cols))

	for idx := range row {
		value := row[idx]
		check(value.Type == schema.Cols[idx].Type)
		if !slices.Contains(schema.PKey, idx) {
			val = row[idx].Encode(val)
		}
	}
	return val
}

func (row Row) DecodeKey(schema *Schema, key []byte) (err error) {
	prefixLen := len(schema.Table) + 1
	if len(key) < prefixLen {
		return errors.New("key too short")
	}
	key = key[prefixLen:]

	check(len(row) == len(schema.Cols))

	for idx := range row {
		row[idx].Type = schema.Cols[idx].Type
		if slices.Contains(schema.PKey, idx) {
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

	for idx := range row {
		if !slices.Contains(schema.PKey, idx) {
			row[idx].Type = schema.Cols[idx].Type
			val, err = row[idx].Decode(val)
			if err != nil {
				return err
			}
		}
	}
	return nil
}
