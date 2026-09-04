package kv

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func evaluationFixture() (*Schema, Row) {
	schema := &Schema{
		Table: "numbers",
		Cols: []Column{
			{Name: "a", Type: TypeI64},
			{Name: "b", Type: TypeI64},
			{Name: "c", Type: TypeI64},
			{Name: "e", Type: TypeI64},
			{Name: "label", Type: TypeStr},
		},
	}

	row := Row{
		{Type: TypeI64, I64: 20},
		{Type: TypeI64, I64: 5},
		{Type: TypeI64, I64: 3},
		{Type: TypeI64, I64: 2},
		{Type: TypeStr, Str: []byte("Sales")},
	}

	return schema, row
}

func TestEvalExprLiteral(t *testing.T) {
	schema, row := evaluationFixture()
	literal := &Cell{Type: TypeI64, I64: 7}

	t.Logf("[INPUT] literal node=%s", exprNodeLabel(literal))
	t.Log("[EVALUATION] A literal is already a value, so evalExpr returns it directly.")

	result, err := evalExpr(schema, row, literal)
	require.NoError(t, err)

	t.Logf("[RESULT] type=%d integer=%d same pointer=%t", result.Type, result.I64, result == literal)
	assert.Same(t, literal, result)
	assert.Equal(t, int64(7), result.I64)
}

func TestEvalExprColumnReference(t *testing.T) {
	schema, row := evaluationFixture()

	t.Logf("[SCHEMA] columns=%v", []string{"a", "b", "c", "e", "label"})
	t.Logf("[ROW] values=[%d, %d, %d, %d, %q]", row[0].I64, row[1].I64, row[2].I64, row[3].I64, row[4].Str)
	t.Log(`[INPUT] column expression="b"`)
	t.Log("[LOOKUP] b is schema column index 1, so evaluation should return row[1].")

	result, err := evalExpr(schema, row, "b")
	require.NoError(t, err)

	t.Logf("[RESULT] column=b index=1 type=%d integer=%d", result.Type, result.I64)
	assert.Same(t, &row[1], result)
	assert.Equal(t, &Cell{Type: TypeI64, I64: 5}, result)
}

func TestEvalExprSimpleSubtraction(t *testing.T) {
	schema, row := evaluationFixture()
	expr := &ExprBinOp{
		op:    OP_SUB,
		left:  "a",
		right: "b",
	}

	t.Log("[EXPRESSION] a - b")
	t.Logf("[TREE]\n%s", renderExprTree(expr))
	t.Log("[FLOW] evaluate a → 20; evaluate b → 5; apply OP_SUB → 20 - 5")

	result, err := evalExpr(schema, row, expr)
	require.NoError(t, err)

	t.Logf("[RESULT] 20 - 5 = %d", result.I64)
	assert.Equal(t, &Cell{Type: TypeI64, I64: 15}, result)
}

func TestEvalExprNestedTree(t *testing.T) {
	schema, row := evaluationFixture()
	input := "a - b + c - e"
	parser := NewParser(input)

	t.Logf("[EXPRESSION INPUT] %q", input)
	expr, err := parser.parseAdd()
	require.NoError(t, err)
	require.True(t, parser.isEnd())

	t.Log("[GROUPING] (((a - b) + c) - e)")
	t.Logf("[TREE]\n%s", renderExprTree(expr))
	t.Log("[ROW VALUES] a=20 b=5 c=3 e=2")
	t.Log("[STEP 1] a - b = 20 - 5 = 15")
	t.Log("[STEP 2] previous result + c = 15 + 3 = 18")
	t.Log("[STEP 3] previous result - e = 18 - 2 = 16")

	result, err := evalExpr(schema, row, expr)
	require.NoError(t, err)

	t.Logf("[FINAL RESULT] type=%d integer=%d", result.Type, result.I64)
	assert.Equal(t, &Cell{Type: TypeI64, I64: 16}, result)
}

func TestEvalExprUnaryNegation(t *testing.T) {
	schema, row := evaluationFixture()
	parser := NewParser("-a")

	t.Log("[EXPRESSION] -a")
	t.Log("[ROW VALUE] a=20")
	expr, err := parser.parseExpr()
	require.NoError(t, err)
	require.True(t, parser.isEnd())
	t.Logf("[TREE]\n%s", renderExprTree(expr))
	t.Log("[FLOW] evaluate a → 20; apply OP_NEG → -20")

	result, err := evalExpr(schema, row, expr)
	require.NoError(t, err)

	t.Logf("[RESULT] type=%d integer=%d", result.Type, result.I64)
	assert.Equal(t, &Cell{Type: TypeI64, I64: -20}, result)
}

func TestEvalExprRepeatedUnaryNegation(t *testing.T) {
	schema, row := evaluationFixture()
	parser := NewParser("--a")

	t.Log("[EXPRESSION] --a")
	t.Log("[EXPECTED GROUPING] -(-a)")
	expr, err := parser.parseExpr()
	require.NoError(t, err)
	require.True(t, parser.isEnd())
	t.Logf("[TREE]\n%s", renderExprTree(expr))
	t.Log("[FLOW] inner negation: -20; outer negation: -(-20) = 20")

	result, err := evalExpr(schema, row, expr)
	require.NoError(t, err)

	t.Logf("[RESULT] type=%d integer=%d", result.Type, result.I64)
	assert.Equal(t, &Cell{Type: TypeI64, I64: 20}, result)
}

func TestEvalExprLogicalNot(t *testing.T) {
	schema, row := evaluationFixture()

	tests := []struct {
		name     string
		value    int64
		expected int64
	}{
		{name: "zero becomes true", value: 0, expected: 1},
		{name: "one becomes false", value: 1, expected: 0},
		{name: "nonzero becomes false", value: 42, expected: 0},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			expr := &ExprUnOp{
				op:  OP_NOT,
				kid: &Cell{Type: TypeI64, I64: test.value},
			}

			t.Logf("[EXPRESSION] NOT %d", test.value)
			t.Logf("[BOOLEAN RULE] 0=false, nonzero=true; NOT should produce %d", test.expected)
			t.Logf("[TREE]\n%s", renderExprTree(expr))

			result, err := evalExpr(schema, row, expr)
			require.NoError(t, err)

			t.Logf("[RESULT] type=%d integer=%d", result.Type, result.I64)
			assert.Equal(t, &Cell{Type: TypeI64, I64: test.expected}, result)
		})
	}
}

func TestEvalExprRejectsUnaryString(t *testing.T) {
	schema, row := evaluationFixture()
	expr := &ExprUnOp{
		op:  OP_NEG,
		kid: &Cell{Type: TypeStr, Str: []byte("Sales")},
	}

	t.Log("[EXPRESSION] -\"Sales\"")
	t.Log("[TYPE CHECK] Unary negation is defined only for TypeI64.")
	t.Logf("[TREE]\n%s", renderExprTree(expr))

	result, err := evalExpr(schema, row, expr)

	t.Logf("[ERROR RESULT] result=%v error=%v", result, err)
	require.EqualError(t, err, "bad unary op")
	assert.Nil(t, result)
}

func TestEvalExprRejectsTypeMismatch(t *testing.T) {
	schema, row := evaluationFixture()
	expr := &ExprBinOp{
		op:    OP_ADD,
		left:  &Cell{Type: TypeI64, I64: 123},
		right: &Cell{Type: TypeStr, Str: []byte("abc")},
	}

	t.Log("[EXPRESSION] 123 + \"abc\"")
	t.Logf("[TREE]\n%s", renderExprTree(expr))
	t.Log("[TYPE CHECK] left=TypeI64 right=TypeStr; strong typing must reject the operation.")

	result, err := evalExpr(schema, row, expr)

	t.Logf("[ERROR RESULT] result=%v error=%v", result, err)
	require.EqualError(t, err, "binary op type mismatch")
	assert.Nil(t, result)
}

func TestEvalExprRejectsUnknownColumn(t *testing.T) {
	schema, row := evaluationFixture()

	t.Log(`[EXPRESSION] column reference="missing"`)
	t.Log("[LOOKUP] No schema column has that name, so evaluation must fail.")

	result, err := evalExpr(schema, row, "missing")

	t.Logf("[ERROR RESULT] result=%v error=%v", result, err)
	require.EqualError(t, err, "unknown column")
	assert.Nil(t, result)
}

func TestEvalExprRejectsUnsupportedOperation(t *testing.T) {
	schema, row := evaluationFixture()
	expr := &ExprBinOp{
		op:    OP_SUB,
		left:  &Cell{Type: TypeStr, Str: []byte("Sales")},
		right: &Cell{Type: TypeStr, Str: []byte("S")},
	}

	t.Log("[EXPRESSION] \"Sales\" - \"S\"")
	t.Log("[TYPE CHECK] Types match, but subtraction is not defined for TypeStr.")

	result, err := evalExpr(schema, row, expr)

	t.Logf("[ERROR RESULT] result=%v error=%v", result, err)
	require.EqualError(t, err, "bad binary op")
	assert.Nil(t, result)
}
