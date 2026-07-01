package kv

import (
	"bytes"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestKVBasic(t *testing.T) {
	var db KV
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
		'k', '1', 'x', 'x', 'x',
	}

	got := ent.Encode()
	assert.Equal(t, want, got)

	var decoded Entry
	assert.NoError(t, decoded.Decode(bytes.NewReader(got)))
	assert.Equal(t, ent.key, decoded.key)
	assert.Equal(t, ent.val, decoded.val)
}
