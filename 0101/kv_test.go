package kv

import (
	"bytes"
	"testing"
)

func TestDatabaseEngineBasics(t *testing.T) {
	var db KV
	if err := db.Open(); err != nil {
		t.Fatalf("Failed to open database: %v", err)
	}
	defer db.Close()

	// 1. Verify Set on a new key returns true
	updated, err := db.Set([]byte("morgankim"), []byte("developer"))
	if err != nil || !updated {
		t.Errorf("Expected updated=true for new key, got %v", updated)
	}

	// 2. Verify Set on an identical duplicate key returns false
	updated, err = db.Set([]byte("morgankim"), []byte("developer"))
	if err != nil || updated {
		t.Errorf("Expected updated=false for duplicate data, got %v", updated)
	}

	// 3. Verify Get fetches correct value
	val, ok, err := db.Get([]byte("morgankim"))
	if err != nil || !ok || !bytes.Equal(val, []byte("developer")) {
		t.Errorf("Get failed! Expected 'developer', got '%s'", val)
	}

	// 4. Verify Del removes key and returns true
	deleted, err := db.Del([]byte("morgankim"))
	if err != nil || !deleted {
		t.Errorf("Expected deleted=true, got %v", deleted)
	}

	// 5. Verify Get returns false after deletion
	_, ok, _ = db.Get([]byte("morgankim"))
	if ok {
		t.Errorf("Expected ok=false for missing key")
	}
}
