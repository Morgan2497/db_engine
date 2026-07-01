package kv

import (
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
