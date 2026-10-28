// Package jobtransfer defines the bounded, digest-verified program transfer
// contract shared by the server and distributed workers.
package jobtransfer

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
	"strings"
)

const (
	Capability               = "chunked_content_v1"
	MaxProgramBytes          = 4 << 20
	MaxChunkBytes            = 64 << 10
	MaxUploadJSONBytes       = 6*MaxProgramBytes + (64 << 10)
	MaxLegacyAssignmentBytes = 8 << 20
	MaxWorkerResponseBytes   = MaxUploadJSONBytes
)

var ErrInvalidManifest = errors.New("invalid job content manifest")

// Manifest names the immutable bytes a worker must fetch and verify before
// connecting to a controller.
type Manifest struct {
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
}

func (m Manifest) Validate() error {
	if m.Size <= 0 || m.Size > MaxProgramBytes || len(m.SHA256) != sha256.Size*2 {
		return ErrInvalidManifest
	}
	decoded, err := hex.DecodeString(m.SHA256)
	if err != nil || len(decoded) != sha256.Size || strings.ToLower(m.SHA256) != m.SHA256 {
		return ErrInvalidManifest
	}
	return nil
}

// ParseRange accepts one explicit inclusive byte range and rejects requests
// that exceed the fixed transfer chunk size or the advertised content size.
func ParseRange(value string, size int64) (start, length int64, err error) {
	if size <= 0 || size > MaxProgramBytes || !strings.HasPrefix(value, "bytes=") {
		return 0, 0, errors.New("invalid content range")
	}
	value = strings.TrimPrefix(value, "bytes=")
	if strings.Contains(value, ",") {
		return 0, 0, errors.New("multiple content ranges are not supported")
	}
	first, last, ok := strings.Cut(value, "-")
	if !ok || first == "" || last == "" {
		return 0, 0, errors.New("an explicit byte range is required")
	}
	start, err = strconv.ParseInt(first, 10, 64)
	if err != nil || start < 0 {
		return 0, 0, errors.New("invalid content range start")
	}
	end, err := strconv.ParseInt(last, 10, 64)
	if err != nil || end < start || end >= size {
		return 0, 0, errors.New("invalid content range end")
	}
	length = end - start + 1
	if length > MaxChunkBytes {
		return 0, 0, fmt.Errorf("content chunk exceeds %d bytes", MaxChunkBytes)
	}
	return start, length, nil
}

// Verify checks the complete downloaded payload against its immutable size
// and digest before callers validate or stream any program bytes.
func Verify(data []byte, manifest Manifest) error {
	if err := manifest.Validate(); err != nil {
		return err
	}
	if int64(len(data)) != manifest.Size {
		return errors.New("job content size does not match manifest")
	}
	sum := sha256.Sum256(data)
	if hex.EncodeToString(sum[:]) != manifest.SHA256 {
		return errors.New("job content digest does not match manifest")
	}
	return nil
}
