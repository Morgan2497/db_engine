package kv

import (
	"errors"
	"slices"
)

func evalExpr(schema *Schema, row Row, expr interface{}) (*Cell, error) {
	switch e := expr.(type) {
	case string:
		idx := slices.IndexFunc(schema.Cols, func(col Column) bool {
			return col.Name == e
		})
		if idx < 0 {
			return nil, errors.New("unknown column")
		}
		return &row[idx], nil

	case *Cell:
		return e, nil

	case *ExprUnOp:
		kid, err := evalExpr(schema, row, e.kid)
		if err != nil {
			return nil, err
		}
		if kid.Type != TypeI64 {
			return nil, errors.New("bad unary op")
		}

		out := &Cell{Type: TypeI64}
		switch e.op {
		case OP_NEG:
			out.I64 = -kid.I64
		case OP_NOT:
			if kid.I64 == 0 {
				out.I64 = 1
			}
		default:
			return nil, errors.New("bad unary op")
		}
		return out, nil

	case *ExprBinOp:
		left, err := evalExpr(schema, row, e.left)
		if err != nil {
			return nil, err
		}

		right, err := evalExpr(schema, row, e.right)
		if err != nil {
			return nil, err
		}

		if left.Type != right.Type {
			return nil, errors.New("binary op type mismatch")
		}

		out := &Cell{Type: left.Type}
		if e.op == OP_ADD && out.Type == TypeStr {
			out.Str = slices.Concat(left.Str, right.Str)
		} else if e.op == OP_ADD && out.Type == TypeI64 {
			out.I64 = left.I64 + right.I64
		} else if e.op == OP_SUB && out.Type == TypeI64 {
			out.I64 = left.I64 - right.I64
		} else if e.op == OP_MUL && out.Type == TypeI64 {
			out.I64 = left.I64 * right.I64
		} else if e.op == OP_DIV && out.Type == TypeI64 {
			out.I64 = left.I64 / right.I64
		} else {
			return nil, errors.New("bad binary op")
		}
		return out, nil
	default:
		panic("unreachable")
	}

}
