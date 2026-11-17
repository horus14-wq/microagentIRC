package config

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestMachineTokens(t *testing.T) {
	dir := t.TempDir()
	path := MachinesPath(filepath.Join(dir, "token"))
	if path != filepath.Join(dir, "machines.json") {
		t.Fatalf("MachinesPath = %q", path)
	}
	if got, err := LoadMachines(path); err != nil || len(got) != 0 {
		t.Fatalf("no file yet: %v %v", got, err)
	}
	when := time.Date(2026, 11, 17, 9, 0, 0, 0, time.UTC)
	token, err := AddMachine(path, "pc2", when)
	if err != nil || len(token) != 64 {
		t.Fatalf("AddMachine: %q %v", token, err)
	}
	raw, err := os.ReadFile(path) // #nosec G304 -- a file this test wrote in t.TempDir
	if err != nil {
		t.Fatal(err)
	}
	// The hub keeps the hash and never the token.
	if strings.Contains(string(raw), token) || !strings.Contains(string(raw), HashToken(token)) {
		t.Fatalf("machines.json holds the token, or not its hash:\n%s", raw)
	}
	if runtime.GOOS != "windows" {
		if info, _ := os.Stat(path); info.Mode().Perm() != 0o600 {
			t.Fatalf("machines.json mode %v, want 0600", info.Mode().Perm())
		}
	}
	got, err := LoadMachines(path)
	if err != nil || got["pc2"].SHA256 != HashToken(token) || got["pc2"].Created != "2026-11-17T09:00:00Z" {
		t.Fatalf("LoadMachines: %+v %v", got, err)
	}
	if _, err := AddMachine(path, "pc2", when); err == nil {
		t.Fatal("a second token for the same machine was made")
	}
	for _, bad := range []string{"", "PC2", "pc 2", "unknown", "pc_2", strings.Repeat("a", 64)} {
		if _, err := AddMachine(path, bad, when); err == nil {
			t.Errorf("AddMachine(%q) accepted", bad)
		}
	}
	if _, err := AddMachine(path, "build-box.example", when); err != nil {
		t.Fatalf("a dotted host name: %v", err)
	}
	if had, err := RevokeMachine(path, "pc2"); err != nil || !had {
		t.Fatalf("RevokeMachine: %v %v", had, err)
	}
	if had, err := RevokeMachine(path, "pc2"); err != nil || had {
		t.Fatalf("a second revoke: %v %v", had, err)
	}
	got, _ = LoadMachines(path)
	if len(got) != 1 || got["build-box.example"].SHA256 == "" {
		t.Fatalf("after the revoke: %+v", got)
	}
	if err := os.WriteFile(path, []byte("{broken"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadMachines(path); err == nil {
		t.Fatal("a broken machines.json was read as a list")
	}
}

func TestWriteTokenReplacesTheFileAndKeepsItPrivate(t *testing.T) {
	path := filepath.Join(t.TempDir(), "token")
	first, err := NewToken()
	if err != nil || len(first) != 64 {
		t.Fatalf("NewToken: %q %v", first, err)
	}
	second, _ := NewToken()
	if first == second {
		t.Fatal("two tokens are equal")
	}
	for _, token := range []string{first, second} {
		if err := WriteToken(path, token); err != nil {
			t.Fatal(err)
		}
		if got, err := ReadToken(path); err != nil || got != token {
			t.Fatalf("ReadToken = %q %v", got, err)
		}
	}
	if runtime.GOOS != "windows" {
		if info, _ := os.Stat(path); info.Mode().Perm() != 0o600 {
			t.Fatalf("token mode %v, want 0600", info.Mode().Perm())
		}
	}
	if _, err := os.Stat(path + ".new"); err == nil {
		t.Fatal("the temporary file was left behind")
	}
}
