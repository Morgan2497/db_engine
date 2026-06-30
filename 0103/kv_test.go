package kv

import (
	"bytes"
	"path/filepath"
	"testing"
)

func TestKVBasic(t *testing.T) {
	path := filepath.Join(t.TempDir(), "basic.log")
	db := &KV{log: Log{FileName: path}}
	if err := db.Open(); err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer db.Close()

	updated, err := db.Set([]byte("morgankim"), []byte("developer"))
	if err != nil || !updated {
		t.Fatalf("Set new key: updated=%v err=%v", updated, err)
	}

	updated, err = db.Set([]byte("morgankim"), []byte("developer"))
	if err != nil || updated {
		t.Fatalf("Set duplicate: updated=%v err=%v", updated, err)
	}

	val, ok, err := db.Get([]byte("morgankim"))
	if err != nil || !ok || !bytes.Equal(val, []byte("developer")) {
		t.Fatalf("Get: ok=%v err=%v val=%q", ok, err, val)
	}

	deleted, err := db.Del([]byte("morgankim"))
	if err != nil || !deleted {
		t.Fatalf("Del existing: deleted=%v err=%v", deleted, err)
	}

	_, ok, err = db.Get([]byte("morgankim"))
	if err != nil || ok {
		t.Fatalf("Get after delete: ok=%v err=%v", ok, err)
	}

	deleted, err = db.Del([]byte("missing"))
	if err != nil || deleted {
		t.Fatalf("Del missing: deleted=%v err=%v", deleted, err)
	}
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
	if !bytes.Equal(got, want) {
		t.Fatalf("Encode:\ngot  %v\nwant %v", got, want)
	}

	var decoded Entry
	if err := decoded.Decode(bytes.NewReader(got)); err != nil {
		t.Fatalf("Decode: %v", err)
	}
	if !bytes.Equal(decoded.key, ent.key) || !bytes.Equal(decoded.val, ent.val) || decoded.deleted {
		t.Fatalf("roundtrip mismatch")
	}
}

func TestEntryTombstone(t *testing.T) {
	ent := Entry{key: []byte("k1"), deleted: true}
	got := ent.Encode()

	var decoded Entry
	if err := decoded.Decode(bytes.NewReader(got)); err != nil {
		t.Fatalf("Decode: %v", err)
	}
	if !bytes.Equal(decoded.key, ent.key) || !decoded.deleted {
		t.Fatalf("tombstone mismatch")
	}
}

func TestKVRecovery(t *testing.T) {
	path := filepath.Join(t.TempDir(), "test.log")

	db1 := &KV{log: Log{FileName: path}}
	if err := db1.Open(); err != nil {
		t.Fatalf("open db1: %v", err)
	}

	if _, err := db1.Set([]byte("user1"), []byte("Morgan")); err != nil {
		t.Fatal(err)
	}
	if _, err := db1.Set([]byte("user2"), []byte("Alice")); err != nil {
		t.Fatal(err)
	}
	if _, err := db1.Set([]byte("user1"), []byte("Morgan Kim")); err != nil {
		t.Fatal(err)
	}
	if _, err := db1.Del([]byte("user2")); err != nil {
		t.Fatal(err)
	}
	if err := db1.Close(); err != nil {
		t.Fatal(err)
	}

	db2 := &KV{log: Log{FileName: path}}
	if err := db2.Open(); err != nil {
		t.Fatalf("open db2: %v", err)
	}
	defer db2.Close()

	val, ok, err := db2.Get([]byte("user1"))
	if err != nil || !ok || string(val) != "Morgan Kim" {
		t.Fatalf("user1: ok=%v err=%v val=%q", ok, err, val)
	}
	if _, ok, err := db2.Get([]byte("user2")); err != nil || ok {
		t.Fatalf("user2 should be deleted: ok=%v err=%v", ok, err)
	}
}

func TestEmptyLogOpen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "empty.log")
	db := &KV{log: Log{FileName: path}}
	if err := db.Open(); err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	if _, ok, err := db.Get([]byte("missing")); err != nil || ok {
		t.Fatalf("expected missing key: ok=%v err=%v", ok, err)
	}
}
