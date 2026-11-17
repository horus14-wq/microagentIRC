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

// A rotated hub token is kept as a machine token of the hub's own machine with an end (mairc-edl): it is live
// before the end and not after, replaces an earlier kept token, and stores a hash, never the token.
func TestKeepPreviousHasAnEnd(t *testing.T) {
	path := MachinesPath(filepath.Join(t.TempDir(), "token"))
	now := time.Date(2026, 11, 17, 18, 0, 0, 0, time.UTC)
	old, _ := NewToken()
	if err := KeepPrevious(path, "hubbox", old, now, now.Add(24*time.Hour)); err != nil {
		t.Fatal(err)
	}
	got, err := LoadMachines(path)
	if err != nil || got["hubbox"].SHA256 != HashToken(old) || got["hubbox"].Until != "2026-11-18T18:00:00Z" {
		t.Fatalf("kept: %+v %v", got, err)
	}
	raw, _ := os.ReadFile(path) // #nosec G304 -- a file this test wrote in t.TempDir
	if strings.Contains(string(raw), old) {
		t.Fatal("machines.json holds the old token itself")
	}
	m := got["hubbox"]
	for _, tc := range []struct {
		at   time.Time
		want bool
	}{
		{now, true},
		{now.Add(24*time.Hour - time.Second), true},
		{now.Add(24 * time.Hour), false},
		{now.Add(48 * time.Hour), false},
	} {
		if m.Live(tc.at) != tc.want {
			t.Errorf("Live at %s = %v, want %v", tc.at.Format(time.RFC3339), !tc.want, tc.want)
		}
	}
	if !(Machine{SHA256: "x"}).Live(now.Add(1000 * time.Hour)) {
		t.Error("a token without an end is not live")
	}
	if (Machine{Until: "not a time"}).Live(now) {
		t.Error("an unreadable end counts as live")
	}
	// A second rotation inside the day replaces the kept token.
	newer, _ := NewToken()
	if err := KeepPrevious(path, "hubbox", newer, now, now.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if got, _ := LoadMachines(path); len(got) != 1 || got["hubbox"].SHA256 != HashToken(newer) {
		t.Fatalf("after a second rotation: %+v", got)
	}
	if err := KeepPrevious(path, "Not A Host", old, now, now); err == nil {
		t.Fatal("an invalid machine name was accepted")
	}
}
