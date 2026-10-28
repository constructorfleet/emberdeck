package jobtransfer

import (
	"crypto/sha256"
	"encoding/hex"
	"testing"
)

func TestManifestValidatesBoundedContent(t *testing.T) {
	sum := sha256.Sum256([]byte("abc"))
	valid := Manifest{Size: 3, SHA256: hex.EncodeToString(sum[:])}
	if err := valid.Validate(); err != nil {
		t.Fatalf("valid manifest rejected: %v", err)
	}
	for name, manifest := range map[string]Manifest{
		"empty":        {SHA256: valid.SHA256},
		"missing hash": {Size: 3},
		"bad hash":     {Size: 3, SHA256: "not-a-digest"},
		"over limit":   {Size: MaxProgramBytes + 1, SHA256: valid.SHA256},
	} {
		t.Run(name, func(t *testing.T) {
			if err := manifest.Validate(); err == nil {
				t.Fatal("invalid manifest accepted")
			}
		})
	}
}

func TestParseRangeEnforcesOneBoundedByteRange(t *testing.T) {
	start, length, err := ParseRange("bytes=3-6", 10)
	if err != nil || start != 3 || length != 4 {
		t.Fatalf("ParseRange() = %d, %d, %v; want 3, 4, nil", start, length, err)
	}
	for _, test := range []struct {
		value string
		size  int64
	}{
		{value: "", size: 10},
		{value: "bytes=0-9,10-10", size: 20},
		{value: "bytes=10-10", size: 10},
		{value: "bytes=-10", size: 10},
		{value: "items=0-1", size: 10},
		{value: "bytes=0-65536", size: MaxChunkBytes + 1},
	} {
		t.Run(test.value, func(t *testing.T) {
			if _, _, err := ParseRange(test.value, test.size); err == nil {
				t.Fatalf("ParseRange(%q) accepted", test.value)
			}
		})
	}
}

func TestVerifyRequiresExactLengthAndDigest(t *testing.T) {
	manifest := Manifest{Size: 3, SHA256: "ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad"}
	if err := Verify([]byte("abc"), manifest); err != nil {
		t.Fatalf("valid bytes rejected: %v", err)
	}
	for name, data := range map[string][]byte{"truncated": []byte("ab"), "altered": []byte("abd"), "extended": []byte("abcd")} {
		t.Run(name, func(t *testing.T) {
			if err := Verify(data, manifest); err == nil {
				t.Fatal("unverified bytes accepted")
			}
		})
	}
}
