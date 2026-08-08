//go:build !unix

package kv

import "os"

func createFileSync(file string) (*os.File, error) {
	return os.OpenFile(file, os.O_RDWR|os.O_CREATE, 0o644)
}
