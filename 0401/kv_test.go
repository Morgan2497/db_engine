package kv

import (
	"bytes"
	"os"
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

func TestKVUpdateMode(t *testing.T) {
	kv := KV{}
	kv.log.FileName = ".test_db"
	defer os.Remove(kv.log.FileName)

	os.Remove(kv.log.FileName)
	err := kv.Open()
	assert.Nil(t, err)
	defer kv.Close()

	updated, err := kv.SetEx([]byte("k1"), []byte("v1"), ModeUpdate)
	assert.True(t, !updated && err == nil)

	updated, err = kv.SetEx([]byte("k1"), []byte("v1"), ModeUpdate)
	assert.True(t, !updated && err == nil)

	updated, err = kv.SetEx([]byte("k1"), []byte("v1"), ModeInsert)
	assert.True(t, updated && err == nil)

	updated, err = kv.SetEx([]byte("k1"), []byte("xx"), ModeInsert)
	assert.True(t, !updated && err == nil)

	updated, err = kv.SetEx([]byte("k1"), []byte("yy"), ModeUpdate)
	assert.True(t, updated && err == nil)

	updated, err = kv.SetEx([]byte("k1"), []byte("zz"), ModeUpsert)
	assert.True(t, updated && err == nil)

	updated, err = kv.SetEx([]byte("k2"), []byte("tt"), ModeUpsert)
	assert.True(t, updated && err == nil)
}

func TestEntryEncodeDecode(t *testing.T) {
	ent := Entry{key: []byte("k1"), val: []byte("xxx")}
	got := ent.Encode()

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

func roundtripEntry(t *testing.T, ent *Entry) {
	t.Helper()

	enc := ent.Encode()
	dec := &Entry{}
	assert.NoError(t, dec.Decode(bytes.NewReader(enc)))
	assert.Equal(t, ent.key, dec.key)
	assert.Equal(t, ent.deleted, dec.deleted)
	if !ent.deleted {
		assert.Equal(t, ent.val, dec.val)
	}
}

func TestEntryCRCRoundtrip(t *testing.T) {
	t.Run("set", func(t *testing.T) {
		roundtripEntry(t, &Entry{
			key: []byte("test_key"),
			val: []byte("test_value"),
		})
	})
	t.Run("tombstone", func(t *testing.T) {
		roundtripEntry(t, &Entry{
			key:     []byte("test_key"),
			deleted: true,
		})
	})
}

func TestBadChecksum(t *testing.T) {
	ent := &Entry{key: []byte("k"), val: []byte("v")}
	enc := ent.Encode()
	enc[0] ^= 0xff

	dec := &Entry{}
	assert.ErrorIs(t, dec.Decode(bytes.NewReader(enc)), ErrBadSum)
}

func TestTornWriteRecovery(t *testing.T) {
	path := filepath.Join(t.TempDir(), "torn.db")

	kv := &KV{log: Log{FileName: path}}
	assert.NoError(t, kv.Open())
	_, err := kv.Set([]byte("key1"), []byte("value1"))
	assert.NoError(t, err)
	assert.NoError(t, kv.Close())

	file, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o644)
	assert.NoError(t, err)
	_, err = file.Write([]byte{0xDE, 0xAD, 0xBE, 0xEF, 0x00})
	assert.NoError(t, err)
	assert.NoError(t, file.Close())

	kv2 := &KV{log: Log{FileName: path}}
	assert.NoError(t, kv2.Open())
	defer kv2.Close()

	val, ok, err := kv2.Get([]byte("key1"))
	assert.NoError(t, err)
	assert.True(t, ok)
	assert.Equal(t, "value1", string(val))
}
