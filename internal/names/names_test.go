package names

import (
	"errors"
	"math/rand/v2"
	"strings"
	"testing"

	"github.com/horus14-wq/microagentIRC/internal/core"
)

func TestValid(t *testing.T) {
	tests := []struct {
		name string
		want bool
	}{
		{"ab", true},
		{"greenfox1", true},
		{"a_b-c9", true},
		{"a-", true},
		{"irc1", true},
		{"roomy", true},
		{strings.Repeat("a", 24), true},
		{"", false},
		{"a", false},
		{strings.Repeat("a", 25), false},
		{"Abc", false},
		{"1abc", false},
		{"-abc", false},
		{"_abc", false},
		{"a b", false},
		{"a.b", false},
		{"room", false},
		{"all", false},
		{"system", false},
		{"irc", false},
	}
	for _, tt := range tests {
		if got := Valid(tt.name); got != tt.want {
			t.Errorf("Valid(%q) = %v, want %v", tt.name, got, tt.want)
		}
	}
}

func TestCheck(t *testing.T) {
	if err := Check("greenfox1"); err != nil {
		t.Errorf("Check(valid) = %v, want nil", err)
	}
	for _, name := range []string{"", "a", "Room", "room", "a b"} {
		if err := Check(name); !errors.Is(err, core.ErrNameInvalid) {
			t.Errorf("Check(%q) = %v, want ErrNameInvalid", name, err)
		}
	}
}

func TestNormalize(t *testing.T) {
	tests := []struct {
		in, want string
	}{
		{"  GreenFox1 ", "greenfox1"},
		{"\tBLUEdOg7\n", "bluedog7"},
		{"already-fine_2", "already-fine_2"},
		{"  ", ""},
	}
	for _, tt := range tests {
		if got := Normalize(tt.in); got != tt.want {
			t.Errorf("Normalize(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

// scriptedRnd returns the queued values in order, then zeros.
func scriptedRnd(values ...int) func(n int) int {
	i := 0
	return func(n int) int {
		if i < len(values) {
			v := values[i]
			i++
			return v
		}
		return 0
	}
}

func TestGenerateScripted(t *testing.T) {
	// colour index 1 = green, animal index 0 = fox, digit index 0 = 1.
	rnd := scriptedRnd(1, 0, 0)
	free := func(string) (bool, error) { return false, nil }
	got, err := Generate(rnd, free)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if got != "greenfox1" {
		t.Errorf("Generate = %q, want %q", got, "greenfox1")
	}
}

func TestGenerateCollisions(t *testing.T) {
	zero := func(int) int { return 0 }

	t.Run("two digits after 20", func(t *testing.T) {
		calls := 0
		taken := func(string) (bool, error) {
			calls++
			return calls <= 20, nil
		}
		got, err := Generate(zero, taken)
		if err != nil {
			t.Fatalf("Generate: %v", err)
		}
		if got != "redfox10" {
			t.Errorf("Generate = %q, want %q", got, "redfox10")
		}
		if calls != 21 {
			t.Errorf("taken calls = %d, want 21", calls)
		}
	})

	t.Run("three digits after 40", func(t *testing.T) {
		calls := 0
		taken := func(string) (bool, error) {
			calls++
			return calls <= 40, nil
		}
		got, err := Generate(zero, taken)
		if err != nil {
			t.Fatalf("Generate: %v", err)
		}
		if got != "redfox100" {
			t.Errorf("Generate = %q, want %q", got, "redfox100")
		}
		if calls != 41 {
			t.Errorf("taken calls = %d, want 41", calls)
		}
	})

	t.Run("error after 1000 tries", func(t *testing.T) {
		calls := 0
		taken := func(string) (bool, error) {
			calls++
			return true, nil
		}
		if _, err := Generate(zero, taken); err == nil {
			t.Fatal("Generate: want error, got nil")
		}
		if calls != 1000 {
			t.Errorf("taken calls = %d, want 1000", calls)
		}
	})

	t.Run("taken error returned unchanged", func(t *testing.T) {
		boom := errors.New("boom")
		taken := func(string) (bool, error) { return false, boom }
		_, err := Generate(zero, taken)
		if !errors.Is(err, boom) {
			t.Errorf("Generate err = %v, want %v", err, boom)
		}
	})
}

func TestGenerateAllValid(t *testing.T) {
	r := rand.New(rand.NewPCG(1, 2))
	rnd := func(n int) int { return r.IntN(n) }
	free := func(string) (bool, error) { return false, nil }
	for i := 0; i < 10000; i++ {
		name, err := Generate(rnd, free)
		if err != nil {
			t.Fatalf("Generate: %v", err)
		}
		if !Valid(name) {
			t.Fatalf("Generate produced invalid name %q", name)
		}
	}
}
