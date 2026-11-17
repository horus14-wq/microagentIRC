package chat

import "strconv"

// The name rules this package needs until internal/names lands; swap these for that package's functions then.

var (
	colours = []string{"red", "green", "blue", "amber", "gold", "grey", "pink", "teal", "violet", "white"}
	animals = []string{"fox", "dog", "cat", "owl", "bee", "elk", "yak", "eel", "ant", "bat"}
)

// reserved names an agent may not take.
var reserved = map[string]bool{"room": true, "all": true, "system": true, "irc": true}

// validName reports whether name matches ^[a-z][a-z0-9_-]{1,23}$ and is not reserved.
var validName = func(name string) bool {
	if len(name) < 2 || len(name) > 24 || reserved[name] {
		return false
	}
	for i := range len(name) {
		c := name[i]
		switch {
		case c >= 'a' && c <= 'z':
		case i > 0 && (c >= '0' && c <= '9' || c == '_' || c == '-'):
		default:
			return false
		}
	}
	return true
}

// generateName returns colour+animal+digit (1-9); with twoDigits, a number 10-99 (after collisions).
var generateName = func(rnd func(n int) int, twoDigits bool) string {
	n := 1 + rnd(9)
	if twoDigits {
		n = 10 + rnd(90)
	}
	return colours[rnd(len(colours))] + animals[rnd(len(animals))] + strconv.Itoa(n)
}
