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
		case OP_OR:
			return "OR"
		case OP_AND:
			return "AND"
		case OP_EQ:
			return "="
		case OP_NE:
			return "!="
		case OP_LE:
			return "<="
		case OP_GE:
			return ">="
		case OP_LT:
			return "<"
		case OP_GT:
			return ">"
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

	case *ExprUnOp:
		switch node.op {
		case OP_NOT:
			return "NOT"
		case OP_NEG:
			return "-"
		default:
			return fmt.Sprintf("unary-operator(%d)", node.op)
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

	switch node := expr.(type) {
	case *ExprBinOp:
		appendExprTree(lines, node.left, nextPrefix, "left", false)
		appendExprTree(lines, node.right, nextPrefix, "right", true)
	case *ExprUnOp:
		appendExprTree(lines, node.kid, nextPrefix, "kid", true)
	}
}

// renderExprTree displays the actual pointer-based expression structure as a
// readable tree. Run tests with -v to see it.
func renderExprTree(expr interface{}) string {
	lines := []string{exprNodeLabel(expr)}
	switch node := expr.(type) {
	case *ExprBinOp:
		appendExprTree(&lines, node.left, "", "left", false)
		appendExprTree(&lines, node.right, "", "right", true)
	case *ExprUnOp:
		appendExprTree(&lines, node.kid, "", "kid", true)
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
	case *ExprUnOp:
		return fmt.Sprintf(
			"(%s %s)",
			exprNodeLabel(node),
			renderExprPrefix(node.kid),
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

func TestParseExprCompletePrecedenceChain(t *testing.T) {
	input := "f or e and not d = a + b * -c"
	parser := NewParser(input)

	t.Logf("[INPUT] %q", input)
	t.Log("[CALL CHAIN] parseOr → parseAnd → parseNot → parseCmp → parseAdd → parseMul → parseNeg → parseAtom")
	t.Log("[GROUPING] f OR (e AND (NOT (d = (a + (b * (-c))))))")

	expr, err := parser.parseExpr()
	require.NoError(t, err)
	require.True(t, parser.isEnd(), "parser stopped at cursor %d", parser.pos)

	expected := &ExprBinOp{
		op:   OP_OR,
		left: "f",
		right: &ExprBinOp{
			op:   OP_AND,
			left: "e",
			right: &ExprUnOp{
				op: OP_NOT,
				kid: &ExprBinOp{
					op:   OP_EQ,
					left: "d",
					right: &ExprBinOp{
						op:   OP_ADD,
						left: "a",
						right: &ExprBinOp{
							op:   OP_MUL,
							left: "b",
							right: &ExprUnOp{
								op:  OP_NEG,
								kid: "c",
							},
						},
					},
				},
			},
		},
	}

	prefix := renderExprPrefix(expr)
	t.Logf("[PREFIX] %s", prefix)
	t.Logf("[TREE]\n%s", renderExprTree(expr))
	t.Logf("[CURSOR] position=%d input length=%d", parser.pos, len(parser.buf))

	assert.Equal(t, "(OR f (AND e (NOT (= d (+ a (* b (- c)))))))", prefix)
	assert.Equal(t, expected, expr)
}

func TestParseExprRepeatedPrefixOperators(t *testing.T) {
	input := "not not - - a"
	parser := NewParser(input)

	t.Logf("[INPUT] %q", input)
	t.Log("[RECURSION] Each NOT calls parseNot again; each unary - calls parseNeg again.")
	t.Log("[GROUPING] NOT (NOT (-(-a)))")

	expr, err := parser.parseExpr()
	require.NoError(t, err)
	require.True(t, parser.isEnd(), "parser stopped at cursor %d", parser.pos)

	expected := &ExprUnOp{
		op: OP_NOT,
		kid: &ExprUnOp{
			op: OP_NOT,
			kid: &ExprUnOp{
				op: OP_NEG,
				kid: &ExprUnOp{
					op:  OP_NEG,
					kid: "a",
				},
			},
		},
	}

	prefix := renderExprPrefix(expr)
	t.Logf("[PREFIX] %s", prefix)
	t.Logf("[TREE]\n%s", renderExprTree(expr))

	assert.Equal(t, "(NOT (NOT (- (- a))))", prefix)
	assert.Equal(t, expected, expr)
}

func TestParseExprNotEqualAliases(t *testing.T) {
	for _, input := range []string{"a != b", "a <> b"} {
		t.Run(input, func(t *testing.T) {
			parser := NewParser(input)
			t.Logf("[INPUT] %q", input)

			expr, err := parser.parseExpr()
			require.NoError(t, err)
			require.True(t, parser.isEnd(), "parser stopped at cursor %d", parser.pos)

			expected := &ExprBinOp{op: OP_NE, left: "a", right: "b"}
			prefix := renderExprPrefix(expr)

			t.Logf("[NORMALIZED OPERATOR] %q maps to OP_NE", input[2:4])
			t.Logf("[PREFIX] %s", prefix)
			t.Logf("[TREE]\n%s", renderExprTree(expr))

			assert.Equal(t, "(!= a b)", prefix)
			assert.Equal(t, expected, expr)
		})
	}
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
		cols:  []interface{}{"a", "b"},
		cond: &ExprBinOp{
			op: OP_AND,
			left: &ExprBinOp{
				op:    OP_EQ,
				left:  "c",
				right: &Cell{Type: TypeI64, I64: 1},
			},
			right: &ExprBinOp{
				op:    OP_EQ,
				left:  "d",
				right: &Cell{Type: TypeStr, Str: []byte("e")},
			},
		},
	}

	testParseSelect(t, query, expectedOutput)
}

func TestParseSelectExpressions(t *testing.T) {
	query := "SELECT a * 4 - b, d + c FROM t WHERE id=1;"

	expectedOutput := StmtSelect{
		table: "t",
		cols: []interface{}{
			&ExprBinOp{
				op: OP_SUB,
				left: &ExprBinOp{
					op:    OP_MUL,
					left:  "a",
					right: &Cell{Type: TypeI64, I64: 4},
				},
				right: "b",
			},
			&ExprBinOp{op: OP_ADD, left: "d", right: "c"},
		},
		cond: &ExprBinOp{
			op:    OP_EQ,
			left:  "id",
			right: &Cell{Type: TypeI64, I64: 1},
		},
	}

	t.Logf("[SQL] %s", query)
	t.Logf("[SELECT EXPRESSION 0]\n%s", renderExprTree(expectedOutput.cols[0]))
	t.Logf("[SELECT EXPRESSION 1]\n%s", renderExprTree(expectedOutput.cols[1]))
	testParseSelect(t, query, expectedOutput)
}

func TestParseUpdateExpressions(t *testing.T) {
	query := "UPDATE t SET a = a - b, b = a, c = d + c WHERE id=1;"
	p := NewParser(query)

	stmt, err := p.parseStmt()
	require.NoError(t, err)
	require.True(t, p.isEnd(), "parser stopped at cursor %d", p.pos)

	update, ok := stmt.(*StmtUpdate)
	require.True(t, ok)

	expected := &StmtUpdate{
		table: "t",
		value: []ExprAssign{
			{column: "a", expr: &ExprBinOp{op: OP_SUB, left: "a", right: "b"}},
			{column: "b", expr: "a"},
			{column: "c", expr: &ExprBinOp{op: OP_ADD, left: "d", right: "c"}},
		},
		cond: &ExprBinOp{
			op:    OP_EQ,
			left:  "id",
			right: &Cell{Type: TypeI64, I64: 1},
		},
	}

	t.Logf("[SQL] %s", query)
	for i, assignment := range update.value {
		t.Logf("[ASSIGNMENT %d] destination=%s expression=%s", i, assignment.column, renderExprPrefix(assignment.expr))
		t.Logf("[ASSIGNMENT %d TREE]\n%s", i, renderExprTree(assignment.expr))
	}
	assert.Equal(t, expected, update)
}
