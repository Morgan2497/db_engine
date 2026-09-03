package kv

import "testing"

func TestBloomFilterTenKeys(t *testing.T) {
	filter := NewBloomFilter(1024, 4)
	keys := [][]byte{
		[]byte("apple"),
		[]byte("banana"),
		[]byte("cherry"),
		[]byte("date"),
		[]byte("elderberry"),
		[]byte("fig"),
		[]byte("grape"),
		[]byte("honeydew"),
		[]byte("kiwi"),
		[]byte("lemon"),
	}

	// A new Bloom filter has no bits set, so every key is absent.
	for _, key := range keys {
		mayContain := filter.MayContain(key)
		t.Logf("[BEFORE ADD] key=%q MayContain=%v", key, mayContain)
		if mayContain {
			t.Fatalf("empty Bloom filter unexpectedly contains %q", key)
		}
	}

	for _, key := range keys {
		filter.Add(key)
		t.Logf("[ADD] key=%q", key)
	}

	// Bloom filters may produce false positives, but never false negatives.
	for _, key := range keys {
		mayContain := filter.MayContain(key)
		t.Logf("[AFTER ADD] key=%q MayContain=%v", key, mayContain)
		if !mayContain {
			t.Fatalf("Bloom filter lost inserted key %q", key)
		}
	}
}
