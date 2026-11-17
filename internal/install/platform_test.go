package install

import (
	"bytes"
	"encoding/json"
	"os"
	"reflect"
	"runtime"
	"strings"
	"testing"
)

func TestPlatformHookPathsAndMerge(t *testing.T) {
	cases := []struct {
		name, binary, command string
		args                  []string
	}{
		{"linux unchanged", "/opt/mairc/bin/mairc", "/opt/mairc/bin/mairc hook --client claude", nil},
		{"mac spaces", "/Users/a b/bin/mairc", "'/Users/a b/bin/mairc' hook --client claude", nil},
		{"mac apostrophe", "/Users/O'Neil/bin/mairc", "'/Users/O'\"'\"'Neil/bin/mairc' hook --client claude", nil},
		{"mac shell characters", "/Users/a $HOME & b/bin/mairc", "'/Users/a $HOME & b/bin/mairc' hook --client claude", nil},
		{"windows drive", `C:\Users\a b\bin\mairc.exe`, `C:\Users\a b\bin\mairc.exe`, []string{"hook", "--client", "claude"}},
		{"windows forward slash", "C:/Users/a b/bin/mairc.exe", "C:/Users/a b/bin/mairc.exe", []string{"hook", "--client", "claude"}},
		{"windows shell characters", `C:\Users\a '$HOME & %TEMP%\mairc.exe`, `C:\Users\a '$HOME & %TEMP%\mairc.exe`, []string{"hook", "--client", "claude"}},
		{"windows UNC", `\\server\share\a b\mairc.exe`, `\\server\share\a b\mairc.exe`, []string{"hook", "--client", "claude"}},
		{"windows UNC forward slash", "//server/share/mairc.exe", "//server/share/mairc.exe", []string{"hook", "--client", "claude"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var got struct {
				Type    string   `json:"type"`
				Command string   `json:"command"`
				Args    []string `json:"args"`
				Timeout int      `json:"timeout"`
			}
			if err := json.Unmarshal(hookHandler(tc.binary).bytes(), &got); err != nil {
				t.Fatal(err)
			}
			if got.Type != "command" || got.Command != tc.command || !reflect.DeepEqual(got.Args, tc.args) || got.Timeout != 5 {
				t.Fatalf("handler = %+v; want command %q, args %v, timeout 5", got, tc.command, tc.args)
			}
			for _, content := range []string{otherMCP, otherSettings} {
				root, err := parse([]byte(content))
				if err != nil {
					t.Fatal(err)
				}
				original := root.bytes()
				edit := hooksEdit(tc.binary, false)
				remove := hooksEdit(tc.binary, true)
				if content == otherMCP {
					edit, remove = mcpEdit(tc.binary, false), mcpEdit(tc.binary, true)
				}
				if changed, ok := edit(root); !changed || !ok || !json.Valid(root.bytes()) {
					t.Fatal("first merge must produce valid JSON")
				}
				if content == otherMCP {
					command, _ := root.get("mcpServers").get("mairc").get("command").stringValue()
					if command != tc.binary {
						t.Fatalf("MCP binary changed: %q, want %q", command, tc.binary)
					}
				}
				first := root.bytes()
				if changed, ok := edit(root); changed || !ok || !bytes.Equal(first, root.bytes()) {
					t.Fatal("second merge must be byte-identical")
				}
				if changed, ok := remove(root); !changed || !ok || !bytes.Equal(original, root.bytes()) {
					t.Fatal("uninstall must preserve every unrelated key and its order")
				}
			}
		})
	}
}

// Keep the original Unix golden bytes exact there, and assert the explicit Windows exec-form contract on Windows.
func nativeTestBinary(binary string) string {
	if runtime.GOOS == "windows" && strings.HasPrefix(binary, "/") {
		return "C:" + strings.ReplaceAll(binary, "/", `\`) + ".exe"
	}
	return binary
}

func nativeFileMode(mode os.FileMode) os.FileMode {
	if runtime.GOOS != "windows" {
		return mode
	}
	// Go exposes Windows' readonly attribute, not Unix owner/group/other permissions.
	if mode&0o200 == 0 {
		return 0o444
	}
	return 0o666
}

func nativeHookWords() (path, count string) {
	if runtime.GOOS != "windows" {
		return testBinary + " hook --client claude", "mairc hook"
	}
	quoted, _ := json.Marshal(nativeTestBinary(testBinary))
	return string(quoted), `"hook"`
}

func nativeGolden(b []byte) []byte {
	if runtime.GOOS != "windows" {
		return b
	}
	binary := nativeTestBinary(testBinary)
	quoted, _ := json.Marshal(binary)
	s := strings.ReplaceAll(string(b), `"command": "/opt/mairc/bin/mairc hook --client claude",`,
		`"command": `+string(quoted)+",\n            \"args\": [\n              \"hook\",\n              \"--client\",\n              \"claude\"\n            ],")
	s = strings.ReplaceAll(s, `"command": "/opt/mairc/bin/mairc"`, `"command": `+string(quoted))
	s = strings.ReplaceAll(s, "MCP (stdio): "+testBinary+" stdio", "MCP (stdio): "+binary+" stdio")
	s = strings.ReplaceAll(s, testBinary+" hook --client plain", binary+" hook --client plain")
	return []byte(s)
}

func TestHookOwnershipDoesNotRemoveUnrelatedCommands(t *testing.T) {
	for _, command := range []string{"echo /opt/mairc hook --client claude", "/usr/bin/other hook --client claude", "/opt/not-mairc hook"} {
		if isOurHook(object("command", str(command))) {
			t.Fatalf("unrelated command classified as ours: %q", command)
		}
	}
	if isOurHook(object("command", str(`C:\Tools\mairc.exe`), "args", array(str("version")))) {
		t.Fatal("a different executable subcommand is not our hook")
	}
}
