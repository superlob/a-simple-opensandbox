// Package fileops implements file and directory operations confined to the workspace.
// It depends only on the security package and knows nothing about command execution.
package fileops

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"time"

	"sandbox/execd/internal/security"
)

// Sentinel errors returned by this package. Callers map them to HTTP status codes.
var (
	ErrNotFound     = errors.New("resource does not exist")
	ErrIsDirectory  = errors.New("path is a directory")
	ErrNotDirectory = errors.New("path is not a directory")
	ErrTooLarge     = errors.New("file exceeds the upload size limit")
	ErrForbidden    = errors.New("operation on the workspace root is not allowed")
	ErrNotEmpty     = errors.New("directory is not empty; set recursive to delete it")
	ErrOutside      = security.ErrOutsideWorkspace
)

// Service performs file operations rooted at Workspace.
type Service struct {
	Workspace      string
	MaxUploadBytes int64
}

// FileInfo describes a file or directory.
type FileInfo struct {
	Path    string    `json:"path"`
	Name    string    `json:"name"`
	Size    int64     `json:"size"`
	IsDir   bool      `json:"is_dir"`
	Mode    string    `json:"mode"`
	ModTime time.Time `json:"mod_time"`
}

// UploadResult is returned after a successful upload.
type UploadResult struct {
	Path string `json:"path"`
	Size int64  `json:"size"`
}

// resolve maps a user path to an absolute path inside the workspace, translating
// security errors into this package's errors where relevant.
func (s *Service) resolve(userPath string) (string, error) {
	p, err := security.ResolvePath(s.Workspace, userPath)
	if err != nil {
		if errors.Is(err, security.ErrOutsideWorkspace) {
			return "", ErrOutside
		}
		return "", err
	}
	return p, nil
}

// Upload writes r to userPath atomically. Missing parent directories are created.
// The write goes to a temporary file in the destination directory and is renamed
// into place only after the whole body is received, so a failed or oversized upload
// leaves no partial file behind and does not replace an existing file.
func (s *Service) Upload(userPath string, r io.Reader) (UploadResult, error) {
	dst, err := s.resolve(userPath)
	if err != nil {
		return UploadResult{}, err
	}
	if security.IsWorkspaceRoot(s.Workspace, dst) {
		return UploadResult{}, ErrForbidden
	}
	if st, err := os.Stat(dst); err == nil && st.IsDir() {
		return UploadResult{}, ErrIsDirectory
	}
	dir := filepath.Dir(dst)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return UploadResult{}, fmt.Errorf("create parent directories: %w", err)
	}
	tmp, err := os.CreateTemp(dir, ".execd-upload-*")
	if err != nil {
		return UploadResult{}, fmt.Errorf("create temp file: %w", err)
	}
	tmpName := tmp.Name()
	committed := false
	defer func() {
		if !committed {
			tmp.Close()
			os.Remove(tmpName)
		}
	}()

	n, err := io.Copy(tmp, io.LimitReader(r, s.MaxUploadBytes+1))
	if err != nil {
		return UploadResult{}, fmt.Errorf("write upload: %w", err)
	}
	if n > s.MaxUploadBytes {
		return UploadResult{}, ErrTooLarge
	}
	if err := tmp.Sync(); err != nil {
		return UploadResult{}, fmt.Errorf("sync upload: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return UploadResult{}, fmt.Errorf("close upload: %w", err)
	}
	if err := os.Chmod(tmpName, 0o644); err != nil {
		return UploadResult{}, err
	}
	if err := os.Rename(tmpName, dst); err != nil {
		return UploadResult{}, fmt.Errorf("commit upload: %w", err)
	}
	committed = true
	return UploadResult{Path: dst, Size: n}, nil
}

// Open returns an open file for download. The caller must close it.
func (s *Service) Open(userPath string) (*os.File, FileInfo, error) {
	p, err := s.resolve(userPath)
	if err != nil {
		return nil, FileInfo{}, err
	}
	st, err := os.Stat(p)
	if err != nil {
		return nil, FileInfo{}, mapErr(err)
	}
	if st.IsDir() {
		return nil, FileInfo{}, ErrIsDirectory
	}
	f, err := os.Open(p)
	if err != nil {
		return nil, FileInfo{}, mapErr(err)
	}
	return f, toInfo(p, st), nil
}

// Stat returns metadata for a file or directory.
func (s *Service) Stat(userPath string) (FileInfo, error) {
	p, err := s.resolve(userPath)
	if err != nil {
		return FileInfo{}, err
	}
	st, err := os.Stat(p)
	if err != nil {
		return FileInfo{}, mapErr(err)
	}
	return toInfo(p, st), nil
}

// MkdirAll creates a directory and any missing parents. Repeated calls succeed.
func (s *Service) MkdirAll(userPath string) (FileInfo, error) {
	p, err := s.resolve(userPath)
	if err != nil {
		return FileInfo{}, err
	}
	if err := os.MkdirAll(p, 0o755); err != nil {
		return FileInfo{}, mapErr(err)
	}
	st, err := os.Stat(p)
	if err != nil {
		return FileInfo{}, mapErr(err)
	}
	if !st.IsDir() {
		return FileInfo{}, ErrNotDirectory
	}
	return toInfo(p, st), nil
}

// List returns the entries of a directory, sorted by name. If limit > 0, at most
// limit entries are returned and truncated reports whether more exist.
func (s *Service) List(userPath string, limit int) (entries []FileInfo, truncated bool, err error) {
	p, err := s.resolve(userPath)
	if err != nil {
		return nil, false, err
	}
	st, err := os.Stat(p)
	if err != nil {
		return nil, false, mapErr(err)
	}
	if !st.IsDir() {
		return nil, false, ErrNotDirectory
	}
	des, err := os.ReadDir(p)
	if err != nil {
		return nil, false, mapErr(err)
	}
	sort.Slice(des, func(i, j int) bool { return des[i].Name() < des[j].Name() })
	out := []FileInfo{}
	for _, de := range des {
		if limit > 0 && len(out) >= limit {
			truncated = true
			break
		}
		fi, err := de.Info()
		if err != nil {
			continue // entry vanished between ReadDir and Info
		}
		out = append(out, toInfo(filepath.Join(p, de.Name()), fi))
	}
	return out, truncated, nil
}

// Delete removes a file, or a directory when recursive is true. Deleting the
// workspace root is always refused.
func (s *Service) Delete(userPath string, recursive bool) error {
	p, err := s.resolve(userPath)
	if err != nil {
		return err
	}
	if security.IsWorkspaceRoot(s.Workspace, p) {
		return ErrForbidden
	}
	st, err := os.Lstat(p)
	if err != nil {
		return mapErr(err)
	}
	if st.IsDir() {
		if !recursive {
			des, err := os.ReadDir(p)
			if err != nil {
				return mapErr(err)
			}
			if len(des) > 0 {
				return ErrNotEmpty
			}
			return mapErr(os.Remove(p))
		}
		return mapErr(os.RemoveAll(p))
	}
	return mapErr(os.Remove(p))
}

func mapErr(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, fs.ErrNotExist) {
		return ErrNotFound
	}
	return err
}

func toInfo(p string, st fs.FileInfo) FileInfo {
	return FileInfo{
		Path:    p,
		Name:    st.Name(),
		Size:    st.Size(),
		IsDir:   st.IsDir(),
		Mode:    st.Mode().String(),
		ModTime: st.ModTime().UTC(),
	}
}
