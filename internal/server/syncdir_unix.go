//go:build !windows

package server

import "os"

// Persist the rename's directory entry as well as the file contents.
func syncDirectory(path string) error {
	dir, err := os.Open(path)
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}
