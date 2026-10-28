package server

// Windows directory handles do not support os.File.Sync. The temporary file is
// flushed and Rename is atomic for visibility, but power-loss persistence of
// the directory entry is not guaranteed by this storage backend on Windows.
func syncDirectory(path string) error { return nil }
