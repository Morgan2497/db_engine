package kv

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestTableByPKey(t *testing.T) {
	db := DB{}
	db.KV.log.FileName = ".test_db"
	defer os.Remove(db.KV.log.FileName)

	os.Remove(db.KV.log.FileName)
	err := db.Open()
	assert.Nil(t, err)
	defer db.Close()

	schema := &Schema{
		Table: "link",
		Cols: []Column{
			{Name: "time", Type: TypeI64},
			{Name: "src", Type: TypeStr},
			{Name: "dst", Type: TypeStr},
		},
		PKey: []int{1, 2}, // (src, dst)
	}

	row := Row{
		Cell{Type: TypeI64, I64: 123},
		Cell{Type: TypeStr, Str: []byte("a")},
		Cell{Type: TypeStr, Str: []byte("b")},
	}
	ok, err := db.Select(schema, row)
	t.Logf("select missing row: ok=%v err=%v", ok, err)
	assert.True(t, !ok && err == nil)

	updated, err := db.Insert(schema, row)
	t.Logf("insert row: updated=%v err=%v", updated, err)
	assert.True(t, updated && err == nil)

	out := Row{
		Cell{},
		Cell{Type: TypeStr, Str: []byte("a")},
		Cell{Type: TypeStr, Str: []byte("b")},
	}
	ok, err = db.Select(schema, out)
	t.Logf("select by pkey: ok=%v row=%v", ok, out)
	assert.True(t, ok && err == nil)
	assert.Equal(t, row, out)

	row[0].I64 = 456
	updated, err = db.Update(schema, row)
	t.Logf("update row: updated=%v err=%v", updated, err)
	assert.True(t, updated && err == nil)

	ok, err = db.Select(schema, out)
	t.Logf("select after update: ok=%v row=%v", ok, out)
	assert.True(t, ok && err == nil)
	assert.Equal(t, row, out)

	deleted, err := db.Delete(schema, row)
	t.Logf("delete row: deleted=%v err=%v", deleted, err)
	assert.True(t, deleted && err == nil)

	ok, err = db.Select(schema, row)
	t.Logf("select after delete: ok=%v err=%v", ok, err)
	assert.True(t, !ok && err == nil)
}

func parseStmt(t *testing.T, s string) interface{} {
	t.Helper()
	t.Logf("sql: %s", s)
	p := NewParser(s)
	stmt, err := p.parseStmt()
	require.Nil(t, err)
	return stmt
}

func logSQLResult(t *testing.T, r SQLResult) {
	t.Helper()
	t.Logf("result: updated=%d header=%v values=%v", r.Updated, r.Header, r.Values)
}

func TestSQLByPKey(t *testing.T) {
	db := DB{}
	db.KV.log.FileName = ".test_db"
	defer os.Remove(db.KV.log.FileName)

	os.Remove(db.KV.log.FileName)
	err := db.Open()
	assert.Nil(t, err)
	defer db.Close()

	s := "create table link (time int64, src string, dst string, primary key (src, dst));"
	_, err = db.ExecStmt(parseStmt(t, s))
	require.Nil(t, err)
	t.Log("create table: ok")

	s = "insert into link values (123, 'bob', 'alice');"
	r, err := db.ExecStmt(parseStmt(t, s))
	require.Nil(t, err)
	logSQLResult(t, r)
	require.Equal(t, 1, r.Updated)

	s = "select time from link where dst = 'alice' and src = 'bob';"
	r, err = db.ExecStmt(parseStmt(t, s))
	require.Nil(t, err)
	require.Equal(t, []Row{{Cell{Type: TypeI64, I64: 123}}}, r.Values)

	s = "update link set time = 456 where dst = 'alice' and src = 'bob';"
	r, err = db.ExecStmt(parseStmt(t, s))
	require.Nil(t, err)
	logSQLResult(t, r)
	require.Equal(t, 1, r.Updated)

	s = "select time from link where dst = 'alice' and src = 'bob';"
	r, err = db.ExecStmt(parseStmt(t, s))
	require.Nil(t, err)
	logSQLResult(t, r)
	require.Equal(t, []Row{{Cell{Type: TypeI64, I64: 456}}}, r.Values)

	// reopen
	t.Log("reopen database")
	err = db.Close()
	require.Nil(t, err)
	db = DB{}
	db.KV.log.FileName = ".test_db"
	err = db.Open()
	require.Nil(t, err)

	s = "delete from link where src = 'bob' and dst = 'alice';"
	r, err = db.ExecStmt(parseStmt(t, s))
	require.Nil(t, err)
	logSQLResult(t, r)
	require.Equal(t, 1, r.Updated)

	s = "select time from link where dst = 'alice' and src = 'bob';"
	r, err = db.ExecStmt(parseStmt(t, s))
	require.Nil(t, err)
	logSQLResult(t, r)
	require.Equal(t, 0, len(r.Values))
}

func TestSQLSelectAndUpdateExpressions(t *testing.T) {
	db := DB{}
	db.KV.log.FileName = ".test_db_0505_expr"
	defer os.Remove(db.KV.log.FileName)

	os.Remove(db.KV.log.FileName)
	require.NoError(t, db.Open())
	defer db.Close()

	_, err := db.ExecStmt(parseStmt(t,
		"CREATE TABLE numbers (id int64, a int64, b int64, c int64, d int64, PRIMARY KEY (id));"))
	require.NoError(t, err)

	r, err := db.ExecStmt(parseStmt(t,
		"INSERT INTO numbers VALUES (1, 10, 3, 2, 1);"))
	require.NoError(t, err)
	require.Equal(t, 1, r.Updated)
	t.Log("[ORIGINAL ROW] id=1 a=10 b=3 c=2 d=1")

	r, err = db.ExecStmt(parseStmt(t,
		"SELECT a * 4 - b, d + c FROM numbers WHERE id=1;"))
	require.NoError(t, err)
	logSQLResult(t, r)
	t.Log("[SELECT FLOW] a * 4 - b = 10 * 4 - 3 = 37; d + c = 1 + 2 = 3")
	require.Equal(t, []Row{{
		{Type: TypeI64, I64: 37},
		{Type: TypeI64, I64: 3},
	}}, r.Values)

	r, err = db.ExecStmt(parseStmt(t,
		"UPDATE numbers SET a = a - b, b = a, c = d + c WHERE id=1;"))
	require.NoError(t, err)
	logSQLResult(t, r)
	require.Equal(t, 1, r.Updated)
	t.Log("[UPDATE READ PHASE] new a=10-3=7, new b=old a=10, new c=1+2=3")
	t.Log("[UPDATE WRITE PHASE] apply a=7, b=10, c=3 only after every expression is evaluated")

	r, err = db.ExecStmt(parseStmt(t,
		"SELECT a, b, c, d FROM numbers WHERE id=1;"))
	require.NoError(t, err)
	logSQLResult(t, r)
	require.Equal(t, []Row{{
		{Type: TypeI64, I64: 7},
		{Type: TypeI64, I64: 10},
		{Type: TypeI64, I64: 3},
		{Type: TypeI64, I64: 1},
	}}, r.Values)
}

func TestIterByPKey(t *testing.T) {
	db := DB{}
	db.KV.log.FileName = ".test_db"
	defer os.Remove(db.KV.log.FileName)

	os.Remove(db.KV.log.FileName)
	err := db.Open()
	assert.Nil(t, err)
	defer db.Close()

	schema := &Schema{
		Table: "t",
		Cols: []Column{
			{Name: "k", Type: TypeI64},
			{Name: "v", Type: TypeI64},
		},
		PKey: []int{0},
	}

	N := int64(10)
	for i := int64(0); i < N; i += 2 {
		row := Row{
			Cell{Type: TypeI64, I64: i},
			Cell{Type: TypeI64, I64: i},
		}
		updated, err := db.Insert(schema, row)
		require.True(t, updated && err == nil)
	}

	for i := int64(-1); i < N+1; i++ {
		row := Row{
			Cell{Type: TypeI64, I64: i},
			Cell{},
		}

		out := []int64{}
		iter, err := db.Seek(schema, row)
		for ; err == nil && iter.Valid(); err = iter.Next() {
			out = append(out, iter.Row()[1].I64)
		}
		require.Nil(t, err)

		expected := []int64{}
		for j := i; j < N; j++ {
			if j >= 0 && j%2 == 0 {
				expected = append(expected, j)
			}
		}
		assert.Equal(t, expected, out)
	}
}

func TestMatchRangeSingleComparison(t *testing.T) {
	t.Parallel()

	schema := &Schema{
		Table: "accounts",
		Cols: []Column{
			{Name: "id", Type: TypeI64},
			{Name: "balance", Type: TypeI64},
		},
		PKey: []int{0},
	}

	tests := []struct {
		name     string
		op       ExprOp
		value    int64
		startCmp ExprOp
		stopCmp  ExprOp
		start    []Cell
		stop     []Cell
	}{
		{
			name: "greater than",
			op:   OP_GT, value: 20,
			startCmp: OP_GT, stopCmp: OP_LE,
			start: []Cell{{Type: TypeI64, I64: 20}},
		},
		{
			name: "greater than or equal",
			op:   OP_GE, value: 20,
			startCmp: OP_GE, stopCmp: OP_LE,
			start: []Cell{{Type: TypeI64, I64: 20}},
		},
		{
			name: "less than",
			op:   OP_LT, value: 40,
			startCmp: OP_GE, stopCmp: OP_LT,
			stop: []Cell{{Type: TypeI64, I64: 40}},
		},
		{
			name: "less than or equal",
			op:   OP_LE, value: 40,
			startCmp: OP_GE, stopCmp: OP_LE,
			stop: []Cell{{Type: TypeI64, I64: 40}},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			condition := &ExprBinOp{
				op:    tc.op,
				left:  "id",
				right: &Cell{Type: TypeI64, I64: tc.value},
			}
			t.Logf("[CONDITION] id %s %d", comparisonName(tc.op), tc.value)
			t.Logf("[TREE] left=column id operator=%s right=integer %d", comparisonName(tc.op), tc.value)

			req, ok := matchRange(schema, condition)
			require.True(t, ok)
			t.Logf("[RANGE REQUEST] startCmp=%s start=%v stopCmp=%s stop=%v", comparisonName(req.StartCmp), req.Start, comparisonName(req.StopCmp), req.Stop)

			assert.Equal(t, tc.startCmp, req.StartCmp)
			assert.Equal(t, tc.stopCmp, req.StopCmp)
			assert.Equal(t, tc.start, req.Start)
			assert.Equal(t, tc.stop, req.Stop)
		})
	}
}

func TestMatchRangeRejectsUnsupportedCondition(t *testing.T) {
	schema := &Schema{
		Table: "accounts",
		Cols:  []Column{{Name: "id", Type: TypeI64}, {Name: "label", Type: TypeStr}},
		PKey:  []int{0},
	}

	tests := []struct {
		name string
		cond interface{}
	}{
		{
			name: "non primary key column",
			cond: &ExprBinOp{op: OP_GT, left: "label", right: &Cell{Type: TypeStr, Str: []byte("Sales")}},
		},
		{
			name: "wrong value type",
			cond: &ExprBinOp{op: OP_GT, left: "id", right: &Cell{Type: TypeStr, Str: []byte("20")}},
		},
		{
			name: "equality handled separately",
			cond: &ExprBinOp{op: OP_EQ, left: "id", right: &Cell{Type: TypeI64, I64: 20}},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Logf("[CONDITION] %s", tc.name)
			req, ok := matchRange(schema, tc.cond)
			t.Logf("[RESULT] request=%v matched=%v", req, ok)
			assert.False(t, ok)
			assert.Nil(t, req)
		})
	}
}

func comparisonName(op ExprOp) string {
	switch op {
	case OP_GT:
		return ">"
	case OP_GE:
		return ">="
	case OP_LT:
		return "<"
	case OP_LE:
		return "<="
	default:
		return "?"
	}
}

func TestMatchRangeCombinedComparisons(t *testing.T) {
	schema := &Schema{
		Table: "accounts",
		Cols:  []Column{{Name: "id", Type: TypeI64}},
		PKey:  []int{0},
	}

	tests := []struct {
		name     string
		lowerOp  ExprOp
		upperOp  ExprOp
		startCmp ExprOp
		stopCmp  ExprOp
	}{
		{name: "exclusive exclusive", lowerOp: OP_GT, upperOp: OP_LT, startCmp: OP_GT, stopCmp: OP_LT},
		{name: "inclusive inclusive", lowerOp: OP_GE, upperOp: OP_LE, startCmp: OP_GE, stopCmp: OP_LE},
		{name: "exclusive inclusive", lowerOp: OP_GT, upperOp: OP_LE, startCmp: OP_GT, stopCmp: OP_LE},
		{name: "inclusive exclusive", lowerOp: OP_GE, upperOp: OP_LT, startCmp: OP_GE, stopCmp: OP_LT},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			condition := &ExprBinOp{
				op: OP_AND,
				left: &ExprBinOp{
					op: tc.lowerOp, left: "id",
					right: &Cell{Type: TypeI64, I64: 20},
				},
				right: &ExprBinOp{
					op: tc.upperOp, left: "id",
					right: &Cell{Type: TypeI64, I64: 40},
				},
			}

			t.Logf("[CONDITION] id %s 20 AND id %s 40", comparisonName(tc.lowerOp), comparisonName(tc.upperOp))
			req, ok := matchRange(schema, condition)
			require.True(t, ok)
			t.Logf("[COMBINED RANGE] start: id %s 20; stop: id %s 40", comparisonName(req.StartCmp), comparisonName(req.StopCmp))

			assert.Equal(t, tc.startCmp, req.StartCmp)
			assert.Equal(t, tc.stopCmp, req.StopCmp)
			assert.Equal(t, []Cell{{Type: TypeI64, I64: 20}}, req.Start)
			assert.Equal(t, []Cell{{Type: TypeI64, I64: 40}}, req.Stop)
		})
	}
}

func TestMatchRangeTupleAndReversedComparison(t *testing.T) {
	schema := &Schema{
		Table: "events",
		Cols: []Column{
			{Name: "tenant", Type: TypeStr},
			{Name: "id", Type: TypeI64},
			{Name: "score", Type: TypeI64},
		},
		PKey: []int{0, 1},
	}

	t.Run("composite primary key tuple", func(t *testing.T) {
		condition := &ExprBinOp{
			op: OP_GE,
			left: &ExprTuple{kids: []interface{}{
				"tenant",
				"id",
			}},
			right: &ExprTuple{kids: []interface{}{
				&Cell{Type: TypeStr, Str: []byte("acme")},
				&Cell{Type: TypeI64, I64: 2},
			}},
		}

		t.Log(`[CONDITION] (tenant, id) >= ("acme", 2)`)
		req, ok := matchRange(schema, condition)
		require.True(t, ok)
		t.Logf("[RANGE PREFIX] startCmp=%s start=%v", comparisonName(req.StartCmp), req.Start)

		assert.Equal(t, OP_GE, req.StartCmp)
		assert.Equal(t, []Cell{
			{Type: TypeStr, Str: []byte("acme")},
			{Type: TypeI64, I64: 2},
		}, req.Start)
	})

	t.Run("literal on left is normalized", func(t *testing.T) {
		condition := &ExprBinOp{
			op:    OP_LT,
			left:  &Cell{Type: TypeStr, Str: []byte("acme")},
			right: "tenant",
		}

		t.Log(`[CONDITION] "acme" < tenant`)
		t.Log(`[NORMALIZED] tenant > "acme"`)
		req, ok := matchRange(schema, condition)
		require.True(t, ok)
		assert.Equal(t, OP_GT, req.StartCmp)
		assert.Equal(t, []Cell{{Type: TypeStr, Str: []byte("acme")}}, req.Start)
	})
}

func TestSQLRangeSelectUpdateDelete(t *testing.T) {
	db := DB{}
	db.KV.log.FileName = filepath.Join(t.TempDir(), "range-sql.log")
	require.NoError(t, db.Open())
	defer db.Close()

	_, err := db.ExecStmt(parseStmt(t,
		"CREATE TABLE accounts (id int64, balance int64, PRIMARY KEY (id));"))
	require.NoError(t, err)

	for _, sql := range []string{
		"INSERT INTO accounts VALUES (10, 100);",
		"INSERT INTO accounts VALUES (20, 200);",
		"INSERT INTO accounts VALUES (30, 300);",
		"INSERT INTO accounts VALUES (40, 400);",
		"INSERT INTO accounts VALUES (50, 500);",
	} {
		result, err := db.ExecStmt(parseStmt(t, sql))
		require.NoError(t, err)
		require.Equal(t, 1, result.Updated)
	}

	t.Log("[SELECT RANGE] id > 20 excludes 20; id <= 40 includes 40")
	result, err := db.ExecStmt(parseStmt(t,
		"SELECT id, balance FROM accounts WHERE id > 20 AND id <= 40;"))
	require.NoError(t, err)
	logSQLResult(t, result)
	assert.Equal(t, []Row{
		{{Type: TypeI64, I64: 30}, {Type: TypeI64, I64: 300}},
		{{Type: TypeI64, I64: 40}, {Type: TypeI64, I64: 400}},
	}, result.Values)

	t.Log("[UPDATE RANGE] update IDs 20, 30, and 40")
	result, err = db.ExecStmt(parseStmt(t,
		"UPDATE accounts SET balance = balance + 1 WHERE id >= 20 AND id < 50;"))
	require.NoError(t, err)
	logSQLResult(t, result)
	assert.Equal(t, 3, result.Updated)

	result, err = db.ExecStmt(parseStmt(t,
		"SELECT id, balance FROM accounts WHERE id >= 20 AND id < 50;"))
	require.NoError(t, err)
	assert.Equal(t, []Row{
		{{Type: TypeI64, I64: 20}, {Type: TypeI64, I64: 201}},
		{{Type: TypeI64, I64: 30}, {Type: TypeI64, I64: 301}},
		{{Type: TypeI64, I64: 40}, {Type: TypeI64, I64: 401}},
	}, result.Values)

	t.Log("[DELETE RANGE] collect IDs 20, 30, and 40, then delete all three without iterator skipping")
	result, err = db.ExecStmt(parseStmt(t,
		"DELETE FROM accounts WHERE id >= 20 AND id <= 40;"))
	require.NoError(t, err)
	logSQLResult(t, result)
	assert.Equal(t, 3, result.Updated)

	result, err = db.ExecStmt(parseStmt(t,
		"SELECT id FROM accounts WHERE id >= 10 AND id <= 50;"))
	require.NoError(t, err)
	t.Logf("[ROWS LEFT] %v", result.Values)
	assert.Equal(t, []Row{
		{{Type: TypeI64, I64: 10}},
		{{Type: TypeI64, I64: 50}},
	}, result.Values)
}

func TestSQLCompositeTupleRange(t *testing.T) {
	db := DB{}
	db.KV.log.FileName = filepath.Join(t.TempDir(), "tuple-range-sql.log")
	require.NoError(t, db.Open())
	defer db.Close()

	_, err := db.ExecStmt(parseStmt(t,
		"CREATE TABLE events (tenant string, id int64, score int64, PRIMARY KEY (tenant, id));"))
	require.NoError(t, err)

	for _, sql := range []string{
		"INSERT INTO events VALUES ('acme', 1, 10);",
		"INSERT INTO events VALUES ('acme', 2, 20);",
		"INSERT INTO events VALUES ('acme', 3, 30);",
		"INSERT INTO events VALUES ('beta', 1, 40);",
	} {
		_, err := db.ExecStmt(parseStmt(t, sql))
		require.NoError(t, err)
	}

	t.Log(`[TUPLE RANGE] (tenant,id) >= ("acme",2) AND (tenant,id) < ("beta",1)`)
	result, err := db.ExecStmt(parseStmt(t,
		"SELECT tenant, id, score FROM events WHERE (tenant, id) >= ('acme', 2) AND (tenant, id) < ('beta', 1);"))
	require.NoError(t, err)
	logSQLResult(t, result)
	assert.Equal(t, []Row{
		{{Type: TypeStr, Str: []byte("acme")}, {Type: TypeI64, I64: 2}, {Type: TypeI64, I64: 20}},
		{{Type: TypeStr, Str: []byte("acme")}, {Type: TypeI64, I64: 3}, {Type: TypeI64, I64: 30}},
	}, result.Values)
}
