package kv

import "hash/fnv"

type BloomFilter struct {
	bits      []byte
	bitCount  uint64
	hashCount uint64
}

func bloomHashes(key []byte) (uint64, uint64) {
	first := fnv.New64a()
	_, _ = first.Write(key)

	second := fnv.New64()
	_, _ = second.Write(key)

	return first.Sum64(), second.Sum64() | 1
}

func (filter *BloomFilter) Add(key []byte) {
	first, second := bloomHashes(key)

	for i := uint64(0); i < filter.hashCount; i++ {
		pos := (first + i*second) % filter.bitCount
		filter.setBit(pos)
	}
}

func (filter *BloomFilter) MayContain(key []byte) bool {
	first, second := bloomHashes(key)

	for i := uint64(0); i < filter.hashCount; i++ {
		pos := (first + second*i) % filter.bitCount

		if !filter.hasBit(pos) {
			return false
		}
	}
	return true
}

func (filter *BloomFilter) setBit(pos uint64) {
	check(pos < filter.bitCount)

	byteIndex := pos / 8
	bitIndex := pos % 8
	mask := byte(1 << bitIndex)

	filter.bits[byteIndex] |= mask
}

func (filter *BloomFilter) hasBit(pos uint64) bool {
	check(pos < filter.bitCount)

	byteIndex := pos / 8
	bitIndex := pos % 8
	mask := byte(1 << bitIndex)

	return filter.bits[byteIndex]&mask != 0
}

func NewBloomFilter(bitCount uint64, hashCount uint64) *BloomFilter {
	check(bitCount > 0)
	check(hashCount > 0)

	byteCount := (bitCount + 7) / 8

	return &BloomFilter{
		bits:      make([]byte, byteCount),
		bitCount:  bitCount,
		hashCount: hashCount,
	}
}
