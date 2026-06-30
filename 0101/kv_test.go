package kv

import (
	"bytes"
	"testing"
)

func TestKVBasic(t *testing.T) {
	var db KV
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
