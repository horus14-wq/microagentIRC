package install

import (
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
	if uninstall {
		if start < 0 {
			return raw, false, ""
		}
		from := start
		if from > 0 && strings.TrimRight(lines[from-1], "\r\n") == "" {
			from-- // the blank line install put before the block
		}
		return []byte(strings.Join(lines[:from], "") + strings.Join(lines[stop+1:], "")), true, ""
	}
	if start >= 0 {
		current := strings.Join(lines[start:stop+1], "")
		if current == block {
			return raw, false, ""
		}
		return []byte(strings.Join(lines[:start], "") + block + strings.Join(lines[stop+1:], "")), true, ""
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

func hex4(r rune) string {
	const digits = "0123456789abcdef"
	return string([]byte{digits[r>>12&0xf], digits[r>>8&0xf], digits[r>>4&0xf], digits[r&0xf]})
}

// tomlMCPServer is mairc's [mcp_servers.mairc] table for TOML-configured CLIs (Codex, Grok).
func tomlMCPServer(binary string) string {
	return "[mcp_servers.mairc]\ncommand = " + tomlString(binary) + "\nargs = [\"stdio\"]\n"
}
