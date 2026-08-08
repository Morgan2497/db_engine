package kv

import (
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// exprNodeLabel converts one expression node into a short label for test logs.
// It deliberately lives in the test file because it is a learning/debugging
// aid, not part of the SQL parser itself.
func exprNodeLabel(expr interface{}) string {
	switch node := expr.(type) {
	case string:
		return fmt.Sprintf("column %q", node)

	case *Cell:
		switch node.Type {
		case TypeI64:
			return fmt.Sprintf("integer %d", node.I64)
		case TypeStr:
			return fmt.Sprintf("string %q", node.Str)
		default:
			return fmt.Sprintf("cell(type=%d)", node.Type)
		}

	case *ExprBinOp:
		switch node.op {
		case OP_ADD:
			return "+"
		case OP_SUB:
			return "-"
		case OP_MUL:
			return "*"
		case OP_DIV:
			return "/"
		default:
			return fmt.Sprintf("operator(%d)", node.op)
		}

	default:
		return fmt.Sprintf("unknown(%T)", expr)
	}
}

// appendExprTree renders one child and recursively renders any children below
// it. prefix contains the tree-drawing indentation inherited from its parent.
func appendExprTree(lines *[]string, expr interface{}, prefix, edge string, last bool) {
	connector := "├──"
	nextPrefix := prefix + "│   "
	if last {
		connector = "└──"
		nextPrefix = prefix + "    "
	}

	*lines = append(*lines, fmt.Sprintf("%s%s %s: %s", prefix, connector, edge, exprNodeLabel(expr)))

	if node, ok := expr.(*ExprBinOp); ok {
		appendExprTree(lines, node.left, nextPrefix, "left", false)
		appendExprTree(lines, node.right, nextPrefix, "right", true)
	}
}

// renderExprTree displays the actual pointer-based ExprBinOp structure as a
// readable tree. Run tests with -v to see it.
func renderExprTree(expr interface{}) string {
	lines := []string{exprNodeLabel(expr)}
	if node, ok := expr.(*ExprBinOp); ok {
		appendExprTree(&lines, node.left, "", "left", false)
		appendExprTree(&lines, node.right, "", "right", true)
	}
	return strings.Join(lines, "\n")
}

// renderExprPrefix writes an expression with the operator before both of its
// operands. Prefix form makes the exact grouping of the parsed tree visible
// without relying on SQL's precedence rules.
func renderExprPrefix(expr interface{}) string {
	switch node := expr.(type) {
	case string:
		return node
	case *Cell:
		switch node.Type {
		case TypeI64:
			return fmt.Sprintf("%d", node.I64)
		case TypeStr:
			return fmt.Sprintf("%q", node.Str)
		default:
			return fmt.Sprintf("cell(type=%d)", node.Type)
		}
	case *ExprBinOp:
		return fmt.Sprintf(
			"(%s %s %s)",
			exprNodeLabel(node),
			renderExprPrefix(node.left),
			renderExprPrefix(node.right),
		)
	default:
		return fmt.Sprintf("unknown(%T)", expr)
	}
}

func TestParseName(t *testing.T) {
	p := NewParser(" a b0 _0_ 123 ")
	name, ok := p.tryName()
	assert.True(t, ok && name == "a")
	name, ok = p.tryName()
	assert.True(t, ok && name == "b0")
	name, ok = p.tryName()
	assert.True(t, ok && name == "_0_")
	_, ok = p.tryName()
	assert.False(t, ok)
}

func TestParseKeyword(t *testing.T) {
	p := NewParser(" select  HELLO ")
	assert.False(t, p.tryKeyword("sel"))
	assert.True(t, p.tryKeyword("SELECT"))
	assert.True(t, p.tryKeyword("hello") && p.isEnd())
}

func testParseValue(t *testing.T, s string, ref Cell) {
	t.Helper()
	p := NewParser(s)
	out := Cell{}
	err := p.parseValue(&out)
	assert.NoError(t, err)
	assert.True(t, p.isEnd())
	assert.Equal(t, ref, out)
}

func TestParseValue(t *testing.T) {
	testParseValue(t, " -123 ", Cell{Type: TypeI64, I64: -123})
	testParseValue(t, ` 'abc\'\"d' `, Cell{Type: TypeStr, Str: []byte("abc'\"d")})
	testParseValue(t, ` "abc\'\"d" `, Cell{Type: TypeStr, Str: []byte("abc'\"d")})
}

func TestParseAtom(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected interface{}
	}{
		{name: "column", input: " price ", expected: "price"},
		{name: "integer", input: " 123 ", expected: &Cell{Type: TypeI64, I64: 123}},
		{name: "negative integer", input: " -123 ", expected: &Cell{Type: TypeI64, I64: -123}},
		{name: "string", input: ` "Sales" `, expected: &Cell{Type: TypeStr, Str: []byte("Sales")}},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			parser := NewParser(test.input)
			t.Logf("[ATOM INPUT] SQL fragment=%q initial cursor=%d", test.input, parser.pos)

			expr, err := parser.parseAtom()
			require.NoError(t, err)

			t.Logf("[ATOM RESULT] concrete type=%T node=%s", expr, exprNodeLabel(expr))
			t.Logf("[ATOM CURSOR] position=%d input length=%d", parser.pos, len(parser.buf))

			assert.Equal(t, test.expected, expr)
			assert.True(t, parser.isEnd(), "parser should consume the complete atom")
		})
	}
}

func TestParseAtomRejectsInvalidToken(t *testing.T) {
	parser := NewParser("@")
	t.Logf("[ATOM INPUT] invalid SQL fragment=%q", parser.buf)

	expr, err := parser.parseAtom()

	t.Logf("[ATOM ERROR] expression=%v error=%v cursor=%d", expr, err, parser.pos)
	require.Error(t, err)
	assert.Nil(t, expr)
}

func TestParseAddBuildsLeftAssociativeTree(t *testing.T) {
	input := "a - b + c - e"
	parser := NewParser(input)

	t.Logf("[EXPRESSION INPUT] %q", input)
	t.Log("[EXPECTED GROUPING] (((a - b) + c) - e)")
	t.Log("[BUILD ORDER] a → (a - b) → ((a - b) + c) → (((a - b) + c) - e)")

	expr, err := parser.parseAdd()
	require.NoError(t, err)
	require.True(t, parser.isEnd(), "parser stopped before the expression ended at cursor %d", parser.pos)

	expected := &ExprBinOp{
		op: OP_SUB,
		left: &ExprBinOp{
			op: OP_ADD,
			left: &ExprBinOp{
				op:    OP_SUB,
				left:  "a",
				right: "b",
			},
			right: "c",
		},
		right: "e",
	}

	t.Logf("[TREE ROOT] concrete type=%T", expr)
	t.Logf("[TREE]\n%s", renderExprTree(expr))
	t.Logf("[CURSOR] position=%d input length=%d", parser.pos, len(parser.buf))

	assert.Equal(t, expected, expr)
}

func TestParseAddRejectsMissingRightAtom(t *testing.T) {
	input := "a +"
	parser := NewParser(input)
	t.Logf("[EXPRESSION INPUT] malformed expression=%q", input)

	expr, err := parser.parseAdd()

	t.Logf("[EXPRESSION ERROR] expression=%v error=%v cursor=%d", expr, err, parser.pos)
	require.Error(t, err)
	assert.Nil(t, expr)
}

func TestParseMulBuildsLeftAssociativeTree(t *testing.T) {
	input := "a * b / c"
	parser := NewParser(input)

	t.Logf("[INPUT] %q", input)
	t.Log("[PARSER LEVEL] parseMul handles only * and /; each operand comes from parseAtom.")
	t.Log("[EXPECTED GROUPING] (a * b) / c")
	t.Log("[BUILD ORDER] a → (* a b) → (/ (* a b) c)")

	expr, err := parser.parseMul()
	require.NoError(t, err)
	require.True(t, parser.isEnd(), "parser stopped at cursor %d", parser.pos)

	expected := &ExprBinOp{
		op: OP_DIV,
		left: &ExprBinOp{
			op:    OP_MUL,
			left:  "a",
			right: "b",
		},
		right: "c",
	}

	t.Logf("[PREFIX] %s", renderExprPrefix(expr))
	t.Logf("[TREE]\n%s", renderExprTree(expr))
	t.Logf("[CURSOR] position=%d input length=%d", parser.pos, len(parser.buf))

	assert.Equal(t, "(/ (* a b) c)", renderExprPrefix(expr))
	assert.Equal(t, expected, expr)
}

func TestParseExprHonorsOperatorPrecedence(t *testing.T) {
	input := "a + b * c - d / e"
	parser := NewParser(input)

	t.Logf("[INPUT] %q", input)
	t.Log("[CALL CHAIN] parseExpr → parseAdd → parseMul → parseAtom")
	t.Log("[PRECEDENCE] parseMul finishes b*c and d/e before parseAdd builds + and - nodes.")
	t.Log("[EXPECTED GROUPING] (a + (b * c)) - (d / e)")

	expr, err := parser.parseExpr()
	require.NoError(t, err)
	require.True(t, parser.isEnd(), "parser stopped at cursor %d", parser.pos)

	expected := &ExprBinOp{
		op: OP_SUB,
		left: &ExprBinOp{
			op:   OP_ADD,
			left: "a",
			right: &ExprBinOp{
				op:    OP_MUL,
				left:  "b",
				right: "c",
			},
		},
		right: &ExprBinOp{
			op:    OP_DIV,
			left:  "d",
			right: "e",
		},
	}

	t.Logf("[PREFIX] %s", renderExprPrefix(expr))
	t.Logf("[TREE]\n%s", renderExprTree(expr))
	t.Logf("[CURSOR] position=%d input length=%d", parser.pos, len(parser.buf))

	assert.Equal(t, "(- (+ a (* b c)) (/ d e))", renderExprPrefix(expr))
	assert.Equal(t, expected, expr)
}

func TestParseExprParenthesesOverridePrecedence(t *testing.T) {
	input := "(a + b) * c"
	parser := NewParser(input)

	t.Logf("[INPUT] %q", input)
	t.Log("[RECURSION] parseAtom consumes (, calls parseExpr for a+b, then requires ).")
	t.Log("[OVERRIDE] The parenthesized + subtree becomes the left atom of multiplication.")
	t.Log("[EXPECTED GROUPING] (a + b) * c")

	expr, err := parser.parseExpr()
	require.NoError(t, err)
	require.True(t, parser.isEnd(), "parser stopped at cursor %d", parser.pos)

	expected := &ExprBinOp{
		op: OP_MUL,
		left: &ExprBinOp{
			op:    OP_ADD,
			left:  "a",
			right: "b",
		},
		right: "c",
	}

	t.Logf("[PREFIX] %s", renderExprPrefix(expr))
	t.Logf("[TREE]\n%s", renderExprTree(expr))
	t.Logf("[CURSOR] position=%d input length=%d", parser.pos, len(parser.buf))

	assert.Equal(t, "(* (+ a b) c)", renderExprPrefix(expr))
	assert.Equal(t, expected, expr)
}

func TestParseExprRejectsMissingClosingParenthesis(t *testing.T) {
	input := "(a + b"
	parser := NewParser(input)

	t.Logf("[INPUT] malformed expression=%q", input)
	t.Log("[FLOW] The inner a+b tree parses, but parseAtom cannot consume the required closing ).")

	expr, err := parser.parseExpr()

	t.Logf("[ERROR] expression=%v error=%v cursor=%d", expr, err, parser.pos)
	require.EqualError(t, err, "expect )")
	assert.Nil(t, expr)
}

func TestParseEqual(t *testing.T) {
	// Test parsing a condition like "column = value"
	p := NewParser(" foo = 123 ")
	out := NamedCell{}
	err := p.parseEqual(&out)

	assert.NoError(t, err)
	assert.Equal(t, "foo", out.column)
	assert.Equal(t, Cell{Type: TypeI64, I64: 123}, out.value)
}

func testParseSelect(t *testing.T, s string, ref StmtSelect) {
	t.Helper()
	p := NewParser(s)
	stmt, err := p.parseStmt()
	assert.NoError(t, err)
	out, ok := stmt.(*StmtSelect)
	assert.True(t, ok)
	assert.True(t, p.isEnd(), "Expected parser to reach the end of the string")
	assert.Equal(t, ref, *out)
}

func TestParseSelect(t *testing.T) {
	// This uses the exact string and expected output we traced earlier!
	query := "SELECT a, b FROM t WHERE c=1 AND d='e';"

	expectedOutput := StmtSelect{
		table: "t",
		cols:  []string{"a", "b"},
		keys: []NamedCell{
			{
				column: "c",
				value:  Cell{Type: TypeI64, I64: 1},
			},
			{
				column: "d",
				value:  Cell{Type: TypeStr, Str: []byte("e")},
			},
		},
	}

	testParseSelect(t, query, expectedOutput)
}
