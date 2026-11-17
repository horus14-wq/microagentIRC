package install

import (
	"path/filepath"
	"strings"
	"testing"
)

// mairc-nlr (3): a JSON config keeps the user's formatting. Install inserts only mairc's entry, in the file's own
// indentation (or compact style); every other byte stays, and uninstall gives back the original bytes exactly.
func TestJSONEditsKeepTheUsersFormatting(t *testing.T) {
	binary := nativeTestBinary(testBinary)
	quoted := string(str(binary).raw)
	cases := []struct {
		name, before, after string
	}{
		{
			name: "four spaces, inline values, escapes",
			before: "{\n    \"theme\": \"caf\\u00e9\",\n    \"mcpServers\": {\n" +
				"        \"other\": {\"command\": \"/usr/bin/other\", \"args\": [\"serve\"]}\n    },\n    \"n\": 1.50e1\n}\n",
			after: "{\n    \"theme\": \"caf\\u00e9\",\n    \"mcpServers\": {\n" +
				"        \"other\": {\"command\": \"/usr/bin/other\", \"args\": [\"serve\"]},\n" +
				"        \"mairc\": {\n            \"command\": " + quoted + ",\n            \"args\": [\n" +
				"                \"stdio\"\n            ]\n        }\n    },\n    \"n\": 1.50e1\n}\n",
		},
		{
			name:   "compact, no final newline",
			before: `{"mcpServers":{"other":{"command":"x"}},"z":[1,2]}`,
			after:  `{"mcpServers":{"other":{"command":"x"},"mairc":{"command":` + quoted + `,"args":["stdio"]}},"z":[1,2]}`,
		},
		{
			name:   "tabs and CRLF, no mcpServers yet",
			before: "{\r\n\t\"theme\": \"dark\"\r\n}\r\n",
			after: "{\r\n\t\"theme\": \"dark\",\r\n\t\"mcpServers\": {\r\n\t\t\"mairc\": {\r\n\t\t\t\"command\": " + quoted +
				",\r\n\t\t\t\"args\": [\r\n\t\t\t\t\"stdio\"\r\n\t\t\t]\r\n\t\t}\r\n\t}\r\n}\r\n",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			home := t.TempDir()
			path := filepath.Join(home, ".gemini", "config", "mcp_config.json")
			write(t, path, tc.before, 0o600)
			o := Options{Binary: binary, Home: home, Only: []string{"agy"}}
			if code, out := runRaw(o); code != 0 {
				t.Fatalf("install = %d, %q", code, out)
			}
			if got := string(read(t, path)); got != tc.after {
				t.Fatalf("after install:\n%s\nwant:\n%s", got, tc.after)
			}
			if code, out := runRaw(o); code != 0 || out != "already installed\n" {
				t.Fatalf("second install = %d, %q", code, out)
			}
			o.Uninstall = true
			if code, out := runRaw(o); code != 0 {
				t.Fatalf("uninstall = %d, %q", code, out)
			}
			if got := string(read(t, path)); got != tc.before {
				t.Fatalf("after uninstall:\n%q\nwant:\n%q", got, tc.before)
			}
		})
	}
}

// Hooks in a hand-formatted settings file: our groups are added in the file's style and removed again exactly.
func TestHookEditsKeepTheUsersFormatting(t *testing.T) {
	home := t.TempDir()
	settings := filepath.Join(home, ".claude", "settings.json")
	before := "{\n\t\"permissions\": {\"allow\": [\"Bash(ls:*)\"], \"deny\": []},\n\t\"hooks\": {\n\t\t\"Stop\": [{\"hooks\": " +
		"[{\"type\": \"command\", \"command\": \"notify-send done\"}]}],\n\t\t\"PostToolUse\": [\n\t\t\t{\"matcher\": \"Edit\", " +
		"\"hooks\": [{\"type\": \"command\", \"command\": \"lint\"}]}\n\t\t]\n\t}\n}\n"
	write(t, settings, before, 0o600)
	o := Options{Binary: nativeTestBinary(testBinary), Home: home, Only: []string{"claude"}}
	if code, out := runRaw(o); code != 0 {
		t.Fatalf("install = %d, %q", code, out)
	}
	got := string(read(t, settings))
	for _, kept := range []string{
		"{\n\t\"permissions\": {\"allow\": [\"Bash(ls:*)\"], \"deny\": []},\n\t\"hooks\": {\n\t\t\"Stop\": [{\"hooks\": ",
		"\t\t\t{\"matcher\": \"Edit\", \"hooks\": [{\"type\": \"command\", \"command\": \"lint\"}]},\n\t\t\t{\n\t\t\t\t\"hooks\": [\n",
	} {
		if !strings.Contains(got, kept) {
			t.Fatalf("lost the user's formatting (%q):\n%s", kept, got)
		}
	}
	o.Uninstall = true
	if code, out := runRaw(o); code != 0 {
		t.Fatalf("uninstall = %d, %q", code, out)
	}
	if got := string(read(t, settings)); got != before {
		t.Fatalf("after uninstall:\n%q\nwant:\n%q", got, before)
	}
}

// A document read and written back without an edit is the same bytes, whatever its whitespace.
func TestRenderRoundTripsTheFile(t *testing.T) {
	for _, doc := range []string{
		"{}", " { } \n", "{\"a\" : 1 , \"b\":[ ] ,\"c\":{ \"d\" :[1 ,2, \"x\\\"y\"]}}\n\n",
		"{\r\n  \"a\": [\r\n    true,\r\n    null\r\n  ]\r\n}", "\t{\"\\u0041\": -0.5E+3}",
	} {
		root, err := parse([]byte(doc))
		if err != nil {
			t.Fatalf("%q: %v", doc, err)
		}
		if got := string(root.render()); got != doc {
			t.Fatalf("round trip of %q = %q", doc, got)
		}
	}
}
