package kv

import (
	"bytes"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestKVBasic(t *testing.T) {
	path := filepath.Join(t.TempDir(), "basic.log")
	db := &KV{log: Log{FileName: path}}
	assert.NoError(t, db.Open())
	defer db.Close()

	updated, err := db.Set([]byte("morgankim"), []byte("developer"))
	assert.NoError(t, err)
	assert.True(t, updated)

	updated, err = db.Set([]byte("morgankim"), []byte("developer"))
	assert.NoError(t, err)
	assert.False(t, updated)

	val, ok, err := db.Get([]byte("morgankim"))
	assert.NoError(t, err)
	assert.True(t, ok)
	assert.Equal(t, []byte("developer"), val)

	deleted, err := db.Del([]byte("morgankim"))
	assert.NoError(t, err)
	assert.True(t, deleted)

	_, ok, err = db.Get([]byte("morgankim"))
	assert.NoError(t, err)
	assert.False(t, ok)

	deleted, err = db.Del([]byte("missing"))
	assert.NoError(t, err)
	assert.False(t, deleted)
}

func TestEntryEncodeDecode(t *testing.T) {
	ent := Entry{key: []byte("k1"), val: []byte("xxx")}
	want := []byte{
		2, 0, 0, 0,
		3, 0, 0, 0,
		0,
		'k', '1', 'x', 'x', 'x',
	}

	got := ent.Encode()
	assert.Equal(t, want, got)

	var decoded Entry
	assert.NoError(t, decoded.Decode(bytes.NewReader(got)))
	assert.Equal(t, ent.key, decoded.key)
	assert.Equal(t, ent.val, decoded.val)
	assert.False(t, decoded.deleted)
}

func TestEntryTombstone(t *testing.T) {
	ent := Entry{key: []byte("k1"), deleted: true}
	got := ent.Encode()

	var decoded Entry
	assert.NoError(t, decoded.Decode(bytes.NewReader(got)))
	assert.Equal(t, ent.key, decoded.key)
	assert.True(t, decoded.deleted)
}

func TestKVRecovery(t *testing.T) {
	path := filepath.Join(t.TempDir(), "test.log")

	db1 := &KV{log: Log{FileName: path}}
	assert.NoError(t, db1.Open())

	_, err := db1.Set([]byte("user1"), []byte("Morgan"))
	assert.NoError(t, err)
	_, err = db1.Set([]byte("user2"), []byte("Alice"))
	assert.NoError(t, err)
	_, err = db1.Set([]byte("user1"), []byte("Morgan Kim"))
	assert.NoError(t, err)
	_, err = db1.Del([]byte("user2"))
	assert.NoError(t, err)
	assert.NoError(t, db1.Close())

	db2 := &KV{log: Log{FileName: path}}
	assert.NoError(t, db2.Open())
	defer db2.Close()

	val, ok, err := db2.Get([]byte("user1"))
	assert.NoError(t, err)
	assert.True(t, ok)
	assert.Equal(t, "Morgan Kim", string(val))

	_, ok, err = db2.Get([]byte("user2"))
	assert.NoError(t, err)
	assert.False(t, ok)
}

func TestEmptyLogOpen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "empty.log")
	db := &KV{log: Log{FileName: path}}
	assert.NoError(t, db.Open())
	defer db.Close()

	_, ok, err := db.Get([]byte("missing"))
	assert.NoError(t, err)
	assert.False(t, ok)
}
