package identity

import (
	"errors"
	"strings"
	"testing"
)

func named(name string) func() (string, error) {
	return func() (string, error) { return name, nil }
}

func TestHostName(t *testing.T) {
	tests := []struct {
		name string
		host func() (string, error)
		want string
	}{
		{"plain", named("buildbox"), "buildbox"},
		{"lowercased and trimmed", named("  BuildBox.Example.NET \n"), "buildbox.example.net"},
		{"other characters become a hyphen", named("my host_1/é"), "my-host-1--"},
		{"cut to 63 characters", named(strings.Repeat("a", 80)), strings.Repeat("a", 63)},
		{"empty", named(""), "unknown"},
		{"unreadable", func() (string, error) { return "", errors.New("no name") }, "unknown"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := HostName(tc.host); got != tc.want {
				t.Fatalf("HostName = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestHostSeatRoundTrip(t *testing.T) {
	tests := []struct {
		seat       string
		wantHost   string
		wantLegacy string
	}{
		{"@buildbox/tmux:%12", "buildbox", "tmux:%12"},
		{"@buildbox/tty:/dev/pts/7", "buildbox", "tty:/dev/pts/7"},
		{"@buildbox/cwd:/work/a@b/c", "buildbox", "cwd:/work/a@b/c"},
		{"@buildbox/env:grok-1", "buildbox", "env:grok-1"},
		// Seats written before hosts were added have none, and are their own legacy form.
		{"tmux:%12", "", "tmux:%12"},
		{"tty:/dev/pts/7", "", "tty:/dev/pts/7"},
		{"cwd:/work/@odd/dir", "", "cwd:/work/@odd/dir"},
		{"", "", ""},
		// A lone mark with no separator is not a host-qualified seat.
		{"@nothing", "", "@nothing"},
	}
	for _, tc := range tests {
		if got := HostOf(tc.seat); got != tc.wantHost {
			t.Errorf("HostOf(%q) = %q, want %q", tc.seat, got, tc.wantHost)
		}
		if got := Legacy(tc.seat); got != tc.wantLegacy {
			t.Errorf("Legacy(%q) = %q, want %q", tc.seat, got, tc.wantLegacy)
		}
	}
	if got := HostSeat("tmux:%12", named("BuildBox")); got != "@buildbox/tmux:%12" {
		t.Fatalf("HostSeat = %q", got)
	}
	// MAIRC_SEAT still comes first, and is qualified like every other seat.
	env := func(key string) string {
		return map[string]string{"MAIRC_SEAT": "grok-1", "TMUX_PANE": "%3"}[key]
	}
	seat := Seat(env, func() (string, bool) { return "/dev/pts/1", true }, func() (string, error) { return "/w", nil })
	if got := HostSeat(seat, named("pc")); got != "@pc/env:grok-1" {
		t.Fatalf("qualified MAIRC_SEAT = %q", got)
	}
}

func TestSessionIDs(t *testing.T) {
	for _, tc := range []struct {
		id   string
		want bool
	}{
		{"0a1b2c3d-4e5f-6789-abcd-ef0123456789", true},
		{"x", true},
		{strings.Repeat("s", 200), true},
		{strings.Repeat("s", 201), false},
		{"", false},
		{"has space", false},
		{"tab\there", false},
		{"non-ascii-é", false},
		{"ctrl\x01", false},
	} {
		if got := ValidSession(tc.id); got != tc.want {
			t.Errorf("ValidSession(%q) = %v, want %v", tc.id, got, tc.want)
		}
	}
	env := func(values map[string]string) func(string) string {
		return func(key string) string { return values[key] }
	}
	for _, tc := range []struct {
		name string
		env  map[string]string
		want string
	}{
		{"none", nil, ""},
		{"claude code", map[string]string{"CLAUDE_CODE_SESSION_ID": "c-1"}, "c-1"},
		{"gemini", map[string]string{"GEMINI_SESSION_ID": "g-1"}, "g-1"},
		{"grok", map[string]string{"GROK_SESSION_ID": "k-1"}, "k-1"},
		{"the first valid one in order", map[string]string{"GEMINI_SESSION_ID": "g-1", "GROK_SESSION_ID": "k-1"}, "g-1"},
		{"an invalid value is skipped", map[string]string{"CLAUDE_CODE_SESSION_ID": "bad id", "GROK_SESSION_ID": "k-1"}, "k-1"},
	} {
		if got := SessionFromEnv(env(tc.env)); got != tc.want {
			t.Errorf("%s: SessionFromEnv = %q, want %q", tc.name, got, tc.want)
		}
	}
}

func TestOwnSeats(t *testing.T) {
	own := OwnSeat("@pc/cwd:/w", "3fa91c0e77aa")
	if own != "@pc/cwd:/w#3fa91c0e" {
		t.Fatalf("OwnSeat = %q", own)
	}
	for _, tc := range []struct{ seat, want string }{
		{own, "@pc/cwd:/w"},
		{OwnSeat(own, "00112233"), "@pc/cwd:/w"},
		{"@pc/cwd:/w", "@pc/cwd:/w"},
		// A "#" that is part of a folder name is not an own-seat part.
		{"@pc/cwd:/w/issue#12", "@pc/cwd:/w/issue#12"},
		{"@pc/cwd:/w#notahexx", "@pc/cwd:/w#notahexx"},
		{"@pc/cwd:/w#3FA91C0E", "@pc/cwd:/w#3FA91C0E"},
		{"", ""},
	} {
		if got := SharedSeat(tc.seat); got != tc.want {
			t.Errorf("SharedSeat(%q) = %q, want %q", tc.seat, got, tc.want)
		}
	}
	if HostOf(own) != "pc" || Legacy(own) != "cwd:/w#3fa91c0e" {
		t.Fatalf("an own seat keeps its machine: %q %q", HostOf(own), Legacy(own))
	}
}
