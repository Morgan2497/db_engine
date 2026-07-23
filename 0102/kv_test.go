package kv

import (
	"bytes"
	"testing"

	"github.com/stretchr/testify/assert"
)

// logKV is a tiny helper so every step prints the same shape of info.
func logKV(t *testing.T, step string, key, val []byte, extra string) {
	t.Helper()
	if val != nil {
		t.Logf("[%s] key=%q val=%q | %s", step, key, val, extra)
	} else {
		t.Logf("[%s] key=%q | %s", step, key, extra)
	}
}

func logEntry(t *testing.T, step string, ent Entry, extra string) {
	t.Helper()
	t.Logf("[%s] key=%q val=%q | %s", step, ent.key, ent.val, extra)
}

func TestKVBasic(t *testing.T) {
	t.Log("=== 0102 KV lifecycle test start (same RAM API as 0101) ===")

	var db KV
	t.Log("calling Open() — allocate empty in-memory map")
	assert.NoError(t, db.Open())
	defer func() {
		t.Log("calling Close() — cleanup (no-op in 0102)")
		db.Close()
	}()

	// --- SET #1: new key ---
	key := []byte("morgankim")
	val := []byte("developer")
	logKV(t, "SET input", key, val, "expect updated=true (new key)")

	updated, err := db.Set(key, val)
	t.Logf("[SET output] updated=%v err=%v", updated, err)
	assert.NoError(t, err)
	assert.True(t, updated)

	// --- SET #2: same key + same value (idempotent) ---
	logKV(t, "SET input", key, val, "expect updated=false (no state change)")

	updated, err = db.Set(key, val)
	t.Logf("[SET output] updated=%v err=%v", updated, err)
	assert.NoError(t, err)
	assert.False(t, updated)

	// --- GET: should exist ---
	logKV(t, "GET input", key, nil, "expect ok=true")

	got, ok, err := db.Get(key)
	t.Logf("[GET output] ok=%v err=%v val=%q", ok, err, got)
	assert.NoError(t, err)
	assert.True(t, ok)
	assert.Equal(t, val, got)

	// --- DEL: should remove ---
	logKV(t, "DEL input", key, nil, "expect deleted=true")

	deleted, err := db.Del(key)
	t.Logf("[DEL output] deleted=%v err=%v", deleted, err)
	assert.NoError(t, err)
	assert.True(t, deleted)

	// --- GET after delete: should miss ---
	logKV(t, "GET input", key, nil, "expect ok=false (key gone)")

	_, ok, err = db.Get(key)
	t.Logf("[GET output] ok=%v err=%v", ok, err)
	assert.NoError(t, err)
	assert.False(t, ok)

	// --- DEL missing key: not an error ---
	missing := []byte("missing")
	logKV(t, "DEL input", missing, nil, "expect deleted=false (key never existed)")

	deleted, err = db.Del(missing)
	t.Logf("[DEL output] deleted=%v err=%v", deleted, err)
	assert.NoError(t, err)
	assert.False(t, deleted)

	t.Log("=== 0102 KV lifecycle test end ===")
}

func TestEntryEncodeDecode(t *testing.T) {
	t.Log("=== 0102 Entry serialization test start ===")

	ent := Entry{key: []byte("k1"), val: []byte("xxx")}
	logEntry(t, "ENCODE input", ent, "key len=2, val len=3")

	want := []byte{
		2, 0, 0, 0,
		3, 0, 0, 0,
		'k', '1', 'x', 'x', 'x',
	}
	t.Logf("[ENCODE expect] wire format: keySize(4) + valSize(4) + key + val")
	t.Logf("[ENCODE expect] bytes=%v", want)

	got := ent.Encode()
	t.Logf("[ENCODE output] len=%d bytes=%v", len(got), got)
	assert.Equal(t, want, got)

	t.Log("calling Decode(bytes.NewReader(got)) — read wire bytes back into Entry")
	var decoded Entry
	err := decoded.Decode(bytes.NewReader(got))
	t.Logf("[DECODE output] err=%v", err)
	assert.NoError(t, err)

	logEntry(t, "DECODE result", decoded, "should match original Entry")
	t.Logf("[DECODE check] key match=%v val match=%v",
		bytes.Equal(ent.key, decoded.key),
		bytes.Equal(ent.val, decoded.val),
	)
	assert.Equal(t, ent.key, decoded.key)
	assert.Equal(t, ent.val, decoded.val)

	t.Log("=== 0102 Entry serialization test end ===")
}
