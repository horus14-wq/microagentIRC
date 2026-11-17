// Package names implements the agent name rule and the colour+animal+digit name generator
// (plan.md §5). It is pure: no database, no clock, no global mutable state.
package names

import (
	"errors"
	"strconv"
	"strings"

	"github.com/horus14-wq/microagentIRC/internal/core"
)

// errExhausted is returned by Generate when every candidate name is taken.
var errExhausted = errors.New("no free name")

var colours = [...]string{
	"red", "green", "blue", "gold", "gray", "pink", "teal", "amber",
	"black", "white", "coral", "olive", "navy", "rose", "jade", "plum",
}

var animals = [...]string{
	"fox", "dog", "cat", "owl", "bee", "ant", "bat", "elk",
	"cod", "hen", "yak", "emu", "wolf", "hawk", "lynx", "crow",
	"seal", "deer", "frog", "hare", "mole", "swan", "toad", "wren",
}

// Valid reports whether name is acceptable: it matches ^[a-z][a-z0-9_-]{1,23}$ (2 to 24 characters,
// a lowercase letter first) and is not reserved.
func Valid(name string) bool {
	if len(name) < 2 || len(name) > 24 || name[0] < 'a' || name[0] > 'z' {
		return false
	}
	for i := 1; i < len(name); i++ {
		c := name[i]
		if c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '_' || c == '-' {
			continue
		}
		return false
	}
	switch name {
	case "room", "all", "system", "irc":
		return false
	}
	return true
}

// Check returns nil for a valid name and core.ErrNameInvalid otherwise.
func Check(name string) error {
	if Valid(name) {
		return nil
	}
	return core.ErrNameInvalid
}

// Normalize trims surrounding whitespace and lowercases ASCII letters. It does not make an invalid
// name valid.
func Normalize(name string) string {
	name = strings.TrimSpace(name)
	b := []byte(name)
	for i, c := range b {
		if c >= 'A' && c <= 'Z' {
			b[i] = c + ('a' - 'A')
		}
	}
	return string(b)
}

// Generate returns a name the taken function does not report as taken.
// It draws colour + animal + one digit 1-9 using rnd; after 20 collisions it uses two digits
// (10-99), after 20 more three digits (100-999), and keeps going with three digits until it finds a
// free one or has tried 1000 times, when it returns an error.
func Generate(rnd func(n int) int, taken func(name string) (bool, error)) (string, error) {
	for attempt := 0; attempt < 1000; attempt++ {
		name := colours[rnd(len(colours))] + animals[rnd(len(animals))]
		switch {
		case attempt < 20:
			name += strconv.Itoa(rnd(9) + 1)
		case attempt < 40:
			name += strconv.Itoa(rnd(90) + 10)
		default:
			name += strconv.Itoa(rnd(900) + 100)
		}
		isTaken, err := taken(name)
		if err != nil {
			return "", err
		}
		if !isTaken {
			return name, nil
		}
	}
	return "", errExhausted
}
