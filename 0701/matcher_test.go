package kv

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMatchAllEqAndMatchPKey(t *testing.T) {
	condition := &ExprBinOp{
		op: OP_AND,
		left: &ExprBinOp{
			op:    OP_EQ,
			left:  "id",
			right: &Cell{Type: TypeI64, I64: 7},
		},
		right: &ExprBinOp{
			op:    OP_EQ,
			left:  "tenant",
			right: &Cell{Type: TypeStr, Str: []byte("acme")},
		},
	}

	t.Logf("[CONDITION TREE]\n%s", renderExprTree(condition))

	keys, ok := matchAllEq(condition, nil)
	t.Logf("[MATCH RESULT] supported=%v keys=%v", ok, keys)

	require.True(t, ok)
	require.Equal(t, []NamedCell{
		{column: "id", value: Cell{Type: TypeI64, I64: 7}},
		{column: "tenant", value: Cell{Type: TypeStr, Str: []byte("acme")}},
	}, keys)

	schema := Schema{
		Table: "accounts",
		Cols: []Column{
			{Name: "id", Type: TypeI64},
			{Name: "tenant", Type: TypeStr},
			{Name: "balance", Type: TypeI64},
		},
		PKey: []int{0, 1},
	}

	lookupRow, err := matchPKey(&schema, condition)
	t.Logf("[LOOKUP ROW] row=%v err=%v", lookupRow, err)

	require.NoError(t, err)
	assert.Equal(t, Row{
		{Type: TypeI64, I64: 7},
		{Type: TypeStr, Str: []byte("acme")},
		{},
	}, lookupRow)
}

func TestMatchAllEqRejectsUnsupportedConditions(t *testing.T) {
	tests := []struct {
		name      string
		condition interface{}
	}{
		{
			name: "or",
			condition: &ExprBinOp{
				op:    OP_OR,
				left:  &ExprBinOp{op: OP_EQ, left: "id", right: &Cell{Type: TypeI64, I64: 7}},
				right: &ExprBinOp{op: OP_EQ, left: "id", right: &Cell{Type: TypeI64, I64: 8}},
			},
		},
		{
			name:      "range",
			condition: &ExprBinOp{op: OP_GT, left: "id", right: &Cell{Type: TypeI64, I64: 7}},
		},
		{
			name: "computed equality",
			condition: &ExprBinOp{
				op:    OP_EQ,
				left:  &ExprBinOp{op: OP_ADD, left: "id", right: &Cell{Type: TypeI64, I64: 1}},
				right: &Cell{Type: TypeI64, I64: 8},
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Logf("[CONDITION TREE]\n%s", renderExprTree(test.condition))

			keys, ok := matchAllEq(test.condition, nil)
			t.Logf("[MATCH RESULT] supported=%v keys=%v", ok, keys)

			assert.False(t, ok)
			assert.Nil(t, keys)
		})
	}
}
