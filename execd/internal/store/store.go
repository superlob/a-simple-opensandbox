// Package store persists task records and task logs under DataDir/tasks/<id>/.
//
// Layout:
//
//	DataDir/tasks/<id>/task.json   record (JSON, written atomically)
//	DataDir/tasks/<id>/stdout.log  captured stdout
//	DataDir/tasks/<id>/stderr.log  captured stderr
package store

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"time"
)

// ErrNotFound is returned when a task record or log does not exist.
var ErrNotFound = errors.New("task does not exist")

// Stream names for log files.
const (
	StreamStdout = "stdout"
	StreamStderr = "stderr"
)

// Record is the persisted form of a background task.
type Record struct {
	ID              string     `json:"id"`
	Argv            []string   `json:"argv"`
	Cwd             string     `json:"cwd"`
	EnvKeys         []string   `json:"env_keys,omitempty"` // names only; values are never persisted
	TimeoutSeconds  float64    `json:"timeout_seconds"`
	Status          string     `json:"status"`
	CreatedAt       time.Time  `json:"created_at"`
	StartedAt       *time.Time `json:"started_at,omitempty"`
	FinishedAt      *time.Time `json:"finished_at,omitempty"`
	ExitCode        *int       `json:"exit_code,omitempty"`
	TimedOut        bool       `json:"timed_out"`
	StdoutTruncated bool       `json:"stdout_truncated"`
	StderrTruncated bool       `json:"stderr_truncated"`
	Error           string     `json:"error,omitempty"`
}

// Store reads and writes task files beneath a data directory.
type Store struct {
	root string // DataDir/tasks
}

// New returns a Store rooted at dataDir/tasks, creating the directory if needed.
func New(dataDir string) (*Store, error) {
	root := filepath.Join(dataDir, "tasks")
	if err := os.MkdirAll(root, 0o700); err != nil {
		return nil, fmt.Errorf("create tasks directory: %w", err)
	}
	return &Store{root: root}, nil
}

var idRe = regexp.MustCompile(`^[0-9a-f-]{8,64}$`)

// TaskDir returns the directory for a task. It rejects IDs that could escape the root.
func (s *Store) TaskDir(id string) (string, error) {
	if !idRe.MatchString(id) {
		return "", ErrNotFound
	}
	return filepath.Join(s.root, id), nil
}

// LogPath returns the path of a task log file.
func (s *Store) LogPath(id, stream string) (string, error) {
	dir, err := s.TaskDir(id)
	if err != nil {
		return "", err
	}
	switch stream {
	case StreamStdout:
		return filepath.Join(dir, "stdout.log"), nil
	case StreamStderr:
		return filepath.Join(dir, "stderr.log"), nil
	}
	return "", fmt.Errorf("unknown stream %q", stream)
}

// SaveRecord writes the record atomically: temp file, fsync, then rename.
func (s *Store) SaveRecord(r Record) error {
	dir, err := s.TaskDir(r.ID)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".task-*.json")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	if _, err := tmp.Write(b); err != nil {
		tmp.Close()
		os.Remove(tmpName)
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		os.Remove(tmpName)
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpName)
		return err
	}
	return os.Rename(tmpName, filepath.Join(dir, "task.json"))
}

// LoadRecord reads one record.
func (s *Store) LoadRecord(id string) (Record, error) {
	dir, err := s.TaskDir(id)
	if err != nil {
		return Record{}, err
	}
	b, err := os.ReadFile(filepath.Join(dir, "task.json"))
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return Record{}, ErrNotFound
		}
		return Record{}, err
	}
	var r Record
	if err := json.Unmarshal(b, &r); err != nil {
		return Record{}, fmt.Errorf("decode task %s: %w", id, err)
	}
	return r, nil
}

// ListRecords returns all readable records. Directories with missing or corrupt
// records are skipped and reported through the returned error count.
func (s *Store) ListRecords() (records []Record, skipped int, err error) {
	des, err := os.ReadDir(s.root)
	if err != nil {
		return nil, 0, err
	}
	for _, de := range des {
		if !de.IsDir() {
			continue
		}
		r, err := s.LoadRecord(de.Name())
		if err != nil {
			skipped++
			continue
		}
		records = append(records, r)
	}
	return records, skipped, nil
}

// DeleteTask removes a task's record and logs.
func (s *Store) DeleteTask(id string) error {
	dir, err := s.TaskDir(id)
	if err != nil {
		return err
	}
	return os.RemoveAll(dir)
}

// OpenLog opens a task log for appending, creating it if needed.
func (s *Store) OpenLog(id, stream string) (*os.File, error) {
	p, err := s.LogPath(id, stream)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return nil, err
	}
	return os.OpenFile(p, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
}

// ReadLog returns up to limit bytes of a log starting at offset, and the offset
// to use for the next call. eof is true when no bytes remain after the returned data.
// A missing log file is reported as an empty log.
func (s *Store) ReadLog(id, stream string, offset, limit int64) (data []byte, next int64, eof bool, err error) {
	p, err := s.LogPath(id, stream)
	if err != nil {
		return nil, 0, false, err
	}
	f, err := os.Open(p)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return []byte{}, offset, true, nil
		}
		return nil, 0, false, err
	}
	defer f.Close()
	if _, err := f.Seek(offset, io.SeekStart); err != nil {
		return nil, 0, false, err
	}
	buf := make([]byte, limit)
	n, err := io.ReadFull(f, buf)
	if err != nil && !errors.Is(err, io.ErrUnexpectedEOF) && !errors.Is(err, io.EOF) {
		return nil, 0, false, err
	}
	next = offset + int64(n)
	// Check whether more data exists beyond what was read.
	var probe [1]byte
	m, _ := f.ReadAt(probe[:], next)
	return buf[:n], next, m == 0, nil
}
