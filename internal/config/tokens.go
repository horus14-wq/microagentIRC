package config

import (
	cryptorand "crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"time"
)

// Tokens. The daemon accepts two kinds of bearer token:
//
//   - the hub token, in the file `token`: the machine the daemon runs on uses it, and it may do everything;
//   - machine tokens, one per other machine that joins this daemon. The hub keeps only their SHA-256, in
//     `machines.json`; the token itself exists only on the machine it was made for. A machine token may act only for
//     seats on that machine, and can be revoked alone.

// machinesFile is the file beside the token that lists the machine tokens.
const machinesFile = "machines.json"

// MachinesPath returns where the machine tokens of the installation with this token file are listed.
func MachinesPath(tokenPath string) string {
	return filepath.Join(filepath.Dir(tokenPath), machinesFile)
}

// Machine is one machine token as the hub stores it.
type Machine struct {
	SHA256  string `json:"sha256"`  // of the token, hex
	Created string `json:"created"` // RFC 3339, UTC
}

// NewToken returns a fresh token: 32 random bytes as 64 hexadecimal characters.
func NewToken() (string, error) {
	var raw [32]byte
	if _, err := cryptorand.Read(raw[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(raw[:]), nil
}

// HashToken returns the SHA-256 of a token, hex: what the hub stores for a machine token.
func HashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// WriteToken replaces the token file with token, mode 0600, by writing beside it and renaming, so that a reader
// never sees half a token.
func WriteToken(path, token string) error {
	return writePrivate(path, []byte(token))
}

// ValidMachine reports whether name can name a machine: what identity.HostName produces (lowercase letters, digits,
// "." and "-", at most 63 characters), and not "unknown", which is what a machine without a readable name is called.
func ValidMachine(name string) bool {
	if name == "" || len(name) > 63 || name == "unknown" {
		return false
	}
	for i := range len(name) {
		c := name[i]
		if (c < 'a' || c > 'z') && (c < '0' || c > '9') && c != '.' && c != '-' {
			return false
		}
	}
	return true
}

// LoadMachines reads the machine tokens. A missing file is an empty list.
func LoadMachines(path string) (map[string]Machine, error) {
	raw, err := os.ReadFile(path) // #nosec G304 -- machines.json in mairc's own data directory
	if errors.Is(err, os.ErrNotExist) {
		return map[string]Machine{}, nil
	}
	if err != nil {
		return nil, err
	}
	out := map[string]Machine{}
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, errors.New(path + ": " + err.Error())
	}
	return out, nil
}

// SaveMachines writes the machine tokens, mode 0600, names in order.
func SaveMachines(path string, machines map[string]Machine) error {
	names := make([]string, 0, len(machines))
	for name := range machines {
		names = append(names, name)
	}
	sort.Strings(names)
	ordered := make([]byte, 0, 128*len(names)+4)
	ordered = append(ordered, "{\n"...)
	for i, name := range names {
		key, err := json.Marshal(name)
		if err != nil {
			return err
		}
		value, err := json.Marshal(machines[name])
		if err != nil {
			return err
		}
		ordered = append(ordered, "  "...)
		ordered = append(ordered, key...)
		ordered = append(ordered, ": "...)
		ordered = append(ordered, value...)
		if i < len(names)-1 {
			ordered = append(ordered, ',')
		}
		ordered = append(ordered, '\n')
	}
	ordered = append(ordered, "}\n"...)
	return writePrivate(path, ordered)
}

// AddMachine makes a token for a machine, records its hash and returns the token. The token is not stored.
func AddMachine(path, name string, now time.Time) (string, error) {
	if !ValidMachine(name) {
		return "", errors.New("machine: the machine's host name in lower case (letters, digits, . and -)")
	}
	machines, err := LoadMachines(path)
	if err != nil {
		return "", err
	}
	if _, exists := machines[name]; exists {
		return "", errors.New("machine " + name + " already has a token: revoke it first")
	}
	token, err := NewToken()
	if err != nil {
		return "", err
	}
	machines[name] = Machine{SHA256: HashToken(token), Created: now.UTC().Format(time.RFC3339)}
	return token, SaveMachines(path, machines)
}

// RevokeMachine removes a machine's token. It reports whether there was one.
func RevokeMachine(path, name string) (bool, error) {
	machines, err := LoadMachines(path)
	if err != nil {
		return false, err
	}
	if _, exists := machines[name]; !exists {
		return false, nil
	}
	delete(machines, name)
	return true, SaveMachines(path, machines)
}

// writePrivate replaces path with data, mode 0600, through a file beside it.
func writePrivate(path string, data []byte) error {
	tmp := path + ".new"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}
