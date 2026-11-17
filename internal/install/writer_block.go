package install

import (
	"strconv"
	"strings"
)

// The block writer: the one writer for text configuration (TOML) and instruction files. It never re-encodes the
// user's content. mairc owns exactly the lines between a begin and an end marker: install appends the block (or
// replaces it on a re-run, in place), uninstall removes it together with the one blank line install put before it.
// A file that already holds one of the block's own headers outside the block (a user wrote [mcp_servers.mairc] by
// hand) is refused rather than guessed at.

// A blockSpec is mairc's block in a text file.
type blockSpec struct {
	begin, end string                     // marker lines; matched by prefix, so the begin line may carry a note
	body       func(binary string) string // the lines between the markers, each ending in "\n"
	conflicts  []string                   // lines that must not appear outside the block (trimmed match)
	// plugin marks a block that is the CLI's chat delivery itself (a plugin file), not an MCP entry or a note:
	// doctor reports it as the CLI's hook.
	plugin bool
}

// tomlMarkers are the block markers for TOML files.
func tomlMarkers() (begin, end string) {
	return "# mairc: begin (managed by `mairc install`; edits inside are replaced)", "# mairc: end"
}

// applyBlock returns the file with mairc's block added, replaced or removed. problem is a refusal to write.
func applyBlock(raw []byte, b *blockSpec, binary string, uninstall bool) (out []byte, edited bool, problem string) {
	lines := splitKeep(string(raw))
	start, stop := -1, -1
	for i, line := range lines {
		trimmed := strings.TrimRight(line, "\r\n")
		switch {
		case start < 0 && strings.HasPrefix(trimmed, markerPrefix(b.begin)):
			start = i
		case start >= 0 && stop < 0 && strings.HasPrefix(trimmed, b.end):
			stop = i
		}
	}
	if start >= 0 && stop < 0 {
		return nil, false, "has a mairc begin marker without an end marker: fix it or use --print"
	}
	for i, line := range lines {
		if start >= 0 && i >= start && i <= stop {
			continue
		}
		for _, c := range b.conflicts {
			if strings.TrimSpace(line) == c {
				return nil, false, "already defines " + c + " outside mairc's block: remove it or use --print"
			}
		}
	}
	block := b.begin + "\n" + b.body(binary) + b.end + "\n"
	// What sits between the markers and is not mairc's: a CLI that rewrites its own TOML file can put its tables
	// before the end marker (Grok 1.0.88 did, with [marketplace]). Those lines are the user's; they are kept, after
	// the block, and never replaced or removed with it.
	foreign := ""
	if start >= 0 {
		foreign = foreignLines(lines[start+1:stop], b)
	}
	if uninstall {
		if start < 0 {
			return raw, false, ""
		}
		from := start
		if from > 0 && strings.TrimRight(lines[from-1], "\r\n") == "" {
			from-- // the blank line install put before the block
		}
		return []byte(strings.Join(lines[:from], "") + foreign + strings.Join(lines[stop+1:], "")), true, ""
	}
	if start >= 0 {
		current := strings.Join(lines[start:stop+1], "")
		if current == block {
			return raw, false, ""
		}
		return []byte(strings.Join(lines[:start], "") + block + foreign + strings.Join(lines[stop+1:], "")), true, ""
	}
	text := string(raw)
	if text != "" {
		if !strings.HasSuffix(text, "\n") {
			text += "\n"
		}
		text += "\n"
	}
	return []byte(text + block), true, ""
}

// foreignLines returns the lines between mairc's markers that belong to somebody else, joined. Only a block that
// names its own table headers (a TOML block: b.conflicts) can hold such lines: every table header that is not one of
// mairc's starts a foreign table, which runs until the next header of mairc's own. A block without headers (an
// instruction file) is mairc's from marker to marker.
func foreignLines(inner []string, b *blockSpec) string {
	if len(b.conflicts) == 0 {
		return ""
	}
	var out strings.Builder
	theirs := false
	for _, line := range inner {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "[") {
			theirs = true
			for _, own := range b.conflicts {
				if trimmed == own {
					theirs = false
				}
			}
		}
		if theirs {
			out.WriteString(line)
			if !strings.HasSuffix(line, "\n") {
				out.WriteString("\n")
			}
		}
	}
	return out.String()
}

// markerPrefix is the part of a begin marker that identifies it, before any note in parentheses.
func markerPrefix(begin string) string {
	if i := strings.Index(begin, " ("); i >= 0 {
		return begin[:i]
	}
	return begin
}

// splitKeep splits text into lines that keep their line endings.
func splitKeep(text string) []string {
	if text == "" {
		return nil
	}
	return strings.SplitAfter(text, "\n")
}

// tomlString is a TOML basic string: quotes, backslashes and control characters escaped.
func tomlString(s string) string {
	var b strings.Builder
	b.WriteByte('"')
	for _, r := range s {
		switch r {
		case '"':
			b.WriteString(`\"`)
		case '\\':
			b.WriteString(`\\`)
		case '\n':
			b.WriteString(`\n`)
		case '\r':
			b.WriteString(`\r`)
		case '\t':
			b.WriteString(`\t`)
		default:
			if r < 0x20 || r == 0x7f {
				b.WriteString(`\u` + hex4(r))
				continue
			}
			b.WriteRune(r)
		}
	}
	b.WriteByte('"')
	return b.String()
}

// tomlUnquote reads back a TOML basic string as tomlString writes it ("..." with \" \\ \n \r \t \uXXXX escapes);
// ok=false for anything else.
func tomlUnquote(quoted string) (string, bool) {
	if len(quoted) < 2 || quoted[0] != '"' || quoted[len(quoted)-1] != '"' {
		return "", false
	}
	body := quoted[1 : len(quoted)-1]
	var b strings.Builder
	for i := 0; i < len(body); i++ {
		c := body[i]
		if c == '"' {
			return "", false
		}
		if c != '\\' {
			b.WriteByte(c)
			continue
		}
		if i+1 >= len(body) {
			return "", false
		}
		i++
		switch body[i] {
		case '"':
			b.WriteByte('"')
		case '\\':
			b.WriteByte('\\')
		case 'n':
			b.WriteByte('\n')
		case 'r':
			b.WriteByte('\r')
		case 't':
			b.WriteByte('\t')
		case 'u':
			if i+4 >= len(body) {
				return "", false
			}
			v, err := strconv.ParseUint(body[i+1:i+5], 16, 16)
			if err != nil {
				return "", false
			}
			b.WriteRune(rune(v))
			i += 4
		default:
			return "", false
		}
	}
	return b.String(), true
}

func hex4(r rune) string {
	const digits = "0123456789abcdef"
	return string([]byte{digits[r>>12&0xf], digits[r>>8&0xf], digits[r>>4&0xf], digits[r&0xf]})
}

// tomlMCPServer is mairc's [mcp_servers.mairc] table for TOML-configured CLIs (Codex, Grok).
func tomlMCPServer(binary string) string {
	return "[mcp_servers.mairc]\ncommand = " + tomlString(binary) + "\nargs = [\"stdio\"]\n"
}
