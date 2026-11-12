package fileops

import (
	"bytes"
	"crypto/sha256"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func newSvc(t *testing.T, max int64) *Service {
	t.Helper()
	return &Service{Workspace: t.TempDir(), MaxUploadBytes: max}
}

func TestUploadRoundTripIntegrity(t *testing.T) {
	s := newSvc(t, 1<<20)
	data := bytes.Repeat([]byte("0123456789"), 5000)
	res, err := s.Upload("sub/dir/f.bin", bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	if res.Size != int64(len(data)) {
		t.Errorf("size %d", res.Size)
	}
	f, _, err := s.Open("sub/dir/f.bin")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	var buf bytes.Buffer
	buf.ReadFrom(f)
	if sha256.Sum256(buf.Bytes()) != sha256.Sum256(data) {
		t.Error("checksum mismatch")
	}
}

func TestUploadTooLargeLeavesNoFile(t *testing.T) {
	s := newSvc(t, 10)
	_, err := s.Upload("big.txt", strings.NewReader(strings.Repeat("x", 11)))
	if !errors.Is(err, ErrTooLarge) {
		t.Fatalf("err = %v", err)
	}
	entries, _ := os.ReadDir(s.Workspace)
	if len(entries) != 0 {
		t.Errorf("residual files: %d", len(entries))
	}
}

func TestOverwriteFailureKeepsOriginal(t *testing.T) {
	s := newSvc(t, 4)
	if _, err := s.Upload("a", strings.NewReader("ok")); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Upload("a", strings.NewReader("way too long")); !errors.Is(err, ErrTooLarge) {
		t.Fatalf("err = %v", err)
	}
	b, _ := os.ReadFile(filepath.Join(s.Workspace, "a"))
	if string(b) != "ok" {
		t.Errorf("original overwritten: %q", b)
	}
}

func TestPathEscapeRejected(t *testing.T) {
	s := newSvc(t, 100)
	if _, err := s.Upload("../x", strings.NewReader("a")); !errors.Is(err, ErrOutside) {
		t.Errorf("upload escape: %v", err)
	}
	if _, err := s.Stat("/etc/passwd"); !errors.Is(err, ErrOutside) {
		t.Errorf("stat absolute outside: %v", err)
	}
}

func TestMkdirIdempotentAndList(t *testing.T) {
	s := newSvc(t, 100)
	for i := 0; i < 2; i++ {
		if _, err := s.MkdirAll("a/b"); err != nil {
			t.Fatalf("mkdir #%d: %v", i, err)
		}
	}
	s.Upload("a/b/z.txt", strings.NewReader("1"))
	s.Upload("a/b/y.txt", strings.NewReader("2"))
	entries, truncated, err := s.List("a/b", 1)
	if err != nil || len(entries) != 1 || !truncated || entries[0].Name != "y.txt" {
		t.Errorf("list: %v %v %v", entries, truncated, err)
	}
}

func TestDeleteSemantics(t *testing.T) {
	s := newSvc(t, 100)
	if err := s.Delete("missing", false); !errors.Is(err, ErrNotFound) {
		t.Errorf("missing: %v", err)
	}
	s.MkdirAll("d")
	s.Upload("d/f", strings.NewReader("x"))
	if err := s.Delete("d", false); !errors.Is(err, ErrNotEmpty) {
		t.Errorf("non-empty dir: %v", err)
	}
	if err := s.Delete("d", true); err != nil {
		t.Errorf("recursive: %v", err)
	}
	if err := s.Delete(".", true); !errors.Is(err, ErrForbidden) {
		t.Errorf("workspace root: %v", err)
	}
}
