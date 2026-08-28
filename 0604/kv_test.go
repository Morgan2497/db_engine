package kv

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
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

func TestKVSeek(t *testing.T) {
	kv := KV{}
	kv.log.FileName = ".test_db"
	defer os.Remove(kv.log.FileName)

	os.Remove(kv.log.FileName)
	err := kv.Open()
	assert.Nil(t, err)
	defer kv.Close()

	keys := []string{"c", "e", "g"}
	vals := []string{"3", "5", "7"}
	for i := range keys {
		_, _ = kv.Set([]byte(keys[i]), []byte(vals[i]))
	}

	iter, err := kv.Seek([]byte("a"))
	require.Nil(t, err)
	for i := range keys {
		assert.True(t, iter.Valid())
		assert.Equal(t, []byte(keys[i]), iter.Key())
		assert.Equal(t, []byte(vals[i]), iter.Val())
		err = iter.Next()
		require.Nil(t, err)
	}
	assert.False(t, iter.Valid())

	err = iter.Prev()
	require.Nil(t, err)
	for i := len(keys) - 1; i >= 0; i-- {
		assert.True(t, iter.Valid())
		assert.Equal(t, []byte(keys[i]), iter.Key())
		assert.Equal(t, []byte(vals[i]), iter.Val())
		err = iter.Prev()
		require.Nil(t, err)
	}
	assert.False(t, iter.Valid())

	iter, err = kv.Seek([]byte("f"))
	require.Nil(t, err)
	assert.True(t, iter.Valid())
	assert.Equal(t, []byte("g"), iter.Key())

	iter, err = kv.Seek([]byte("g"))
	require.Nil(t, err)
	assert.True(t, iter.Valid())
	assert.Equal(t, []byte("g"), iter.Key())

	iter, err = kv.Seek([]byte("h"))
	require.Nil(t, err)
	assert.False(t, iter.Valid())
}

func TestKVRangedAscending(t *testing.T) {
	path := filepath.Join(t.TempDir(), "range-ascending.log")

	kv := &KV{
		log: Log{FileName: path},
	}

	require.NoError(t, kv.Open())
	defer kv.Close()

	// Physical keys stored in sorted order by KV.Set.
	keys := []string{"10", "20", "30", "40", "50"}
	t.Logf("[SETUP] inserting keys: %v", keys)

	for _, key := range keys {
		_, err := kv.Set([]byte(key), []byte("value-"+key))
		require.NoError(t, err)
		t.Logf("[INSERT] key=%q value=%q", key, "value-"+key)
	}

	// Seek starts at the first key >= "25", which is "30".
	// "40" is the inclusive stop key.
	t.Log(`[RANGE] request: start="25" stop="40" direction=ascending`)
	iter, err := kv.Range(
		[]byte("25"),
		[]byte("40"),
		false,
	)
	require.NoError(t, err)
	require.True(t, iter.Valid())
	t.Logf(
		"[SEEK] current key=%q (first key >= start)",
		iter.Key(),
	)

	var got []string
	step := 1

	for iter.Valid() {
		t.Logf(
			"[STEP %d] valid=true key=%q value=%q stop=%q",
			step,
			iter.Key(),
			iter.Val(),
			iter.stop,
		)
		got = append(got, string(iter.Key()))

		oldKey := string(iter.Key())
		require.NoError(t, iter.Next())
		t.Logf("[MOVE %d] ascending Next() after key=%q", step, oldKey)
		step++
	}

	if iter.iter.Valid() {
		t.Logf(
			"[STOP] physical key=%q still exists, but it is greater than stop=%q",
			iter.iter.Key(),
			iter.stop,
		)
	} else {
		t.Log("[STOP] physical iterator is outside the KV store")
	}

	t.Logf("[RESULT] collected keys: %v", got)
	assert.Equal(t, []string{"30", "40"}, got)
}

func TestKVRangedDescending(t *testing.T) {
	path := filepath.Join(t.TempDir(), "range-descending.log")

	kv := &KV{
		log: Log{FileName: path},
	}

	require.NoError(t, kv.Open())
	defer kv.Close()

	keys := []string{"10", "20", "30", "40", "50"}
	t.Logf("[SETUP] inserting keys: %v", keys)

	for _, key := range keys {
		_, err := kv.Set([]byte(key), []byte("value-"+key))
		require.NoError(t, err)
		t.Logf("[INSERT] key=%q value=%q", key, "value-"+key)
	}

	// Seek("45") initially lands on "50" because Seek finds the first key >= start.
	// Range corrects that position to "40", the first key <= start.
	t.Log(`[RANGE] request: start="45" stop="20" direction=descending`)
	iter, err := kv.Range(
		[]byte("45"),
		[]byte("20"),
		true,
	)
	require.NoError(t, err)
	require.True(t, iter.Valid())
	t.Logf(
		"[SEEK + CORRECTION] current key=%q (first key <= start)",
		iter.Key(),
	)

	var got []string
	step := 1

	for iter.Valid() {
		t.Logf(
			"[STEP %d] valid=true key=%q value=%q stop=%q",
			step,
			iter.Key(),
			iter.Val(),
			iter.stop,
		)
		got = append(got, string(iter.Key()))

		oldKey := string(iter.Key())
		require.NoError(t, iter.Next())
		t.Logf("[MOVE %d] descending Prev() after key=%q", step, oldKey)
		step++
	}

	if iter.iter.Valid() {
		t.Logf(
			"[STOP] physical key=%q still exists, but it is less than stop=%q",
			iter.iter.Key(),
			iter.stop,
		)
	} else {
		t.Log("[STOP] physical iterator is outside the KV store")
	}

	t.Logf("[RESULT] collected keys: %v", got)
	assert.Equal(t, []string{"40", "30", "20"}, got)
}
