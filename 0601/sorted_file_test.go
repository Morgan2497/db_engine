package kv

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type SortedArray struct {
	keys [][]byte
	vals [][]byte
}

func (arr *SortedArray) Size() int {
	return len(arr.keys)
}

func (arr *SortedArray) Iter() (SortedKVIter, error) {
	return &KVIterator{keys: arr.keys, vals: arr.vals, pos: 0}, nil
}

func TestSortedFile(t *testing.T) {
	fileName := filepath.Join(t.TempDir(), "test.sst")
	file := SortedFile{FileName: fileName}

	keys := [][]byte{[]byte("x"), []byte("y")}
	vals := [][]byte{[]byte("1"), []byte("234")}
	require.NoError(t, file.CreateFromSorted(&SortedArray{keys: keys, vals: vals}))

	data, err := os.ReadFile(fileName)
	require.NoError(t, err)

	expected := []byte{
		2, 0, 0, 0, 0, 0, 0, 0,
		24, 0, 0, 0, 0, 0, 0, 0,
		34, 0, 0, 0, 0, 0, 0, 0,
		1, 0, 0, 0, 1, 0, 0, 0, 'x', '1',
		1, 0, 0, 0, 3, 0, 0, 0, 'y', '2', '3', '4',
	}
	assert.Equal(t, expected, data)
}
