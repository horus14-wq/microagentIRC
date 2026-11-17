//go:build unix

package store

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// The database and its WAL/SHM side files are private (0600), like the token and the pid file, whatever the umask
// and including a database an older version created 0644.
func TestDatabaseFilesArePrivate(t *testing.T) {
	cases := []struct {
		name     string
		existing bool
	}{
		{"new database", false},
		{"existing 0644 database", true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "mairc.db")
			if c.existing {
				s, err := Open(t.Context(), path)
				if err != nil {
					t.Fatal(err)
				}
				_ = s.Close()
				for _, p := range []string{path, path + "-wal", path + "-shm"} {
					if err := os.Chmod(p, 0o644); err != nil && !os.IsNotExist(err) {
						t.Fatal(err)
					}
				}
			}
			s, err := Open(t.Context(), path)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = s.Close() }()
			// A write makes sure the WAL and SHM files exist.
			if err := s.UpsertAgent(t.Context(), "alice", "seat1", time.Now()); err != nil {
				t.Fatal(err)
			}
			for _, p := range []string{path, path + "-wal", path + "-shm"} {
				info, err := os.Stat(p)
				if err != nil {
					t.Fatal(err)
				}
				if mode := info.Mode().Perm(); mode&0o077 != 0 {
					t.Errorf("%s is %v, want no group or other access", filepath.Base(p), mode)
				}
			}
		})
	}
}
