// Package tools holds the four MCP tools agents use (irc_name, irc_send, irc_read, irc_who), on top of core.Chat.
// Every name, description, schema and result text here is served verbatim, so they are kept as short as possible.
package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"

	"github.com/horus14-wq/microagentIRC/internal/core"
)

const (
	badArguments = "bad arguments"
	nothingNew   = "nothing new"
	nobodyYet    = "nobody yet"
	sent         = "sent"
)

// All returns the four tools, in this order: irc_name, irc_send, irc_read, irc_who.
func All(chat core.Chat) []core.Tool {
	return []core.Tool{
		{
			Name:        "irc_name",
			Description: "Set your chat name, or omit name to get one.",
			InputSchema: `{"type":"object","properties":{"name":{"type":"string"}}}`,
			Call: func(ctx context.Context, s *core.Session, args []byte) core.ToolResult {
				var in struct {
					Name *string `json:"name"`
				}
				if !parse(args, &in) {
					return fail(badArguments)
				}
				want := ""
				if in.Name != nil {
					want = *in.Name
				}
				name, err := chat.SetName(ctx, want, s.Seat)
				if err != nil {
					return fail(err.Error())
				}
				s.Name = name
				return ok("you are " + name)
			},
		},
		{
			Name:        "irc_send",
			Description: `Send to the room, or privately: to="a,b".`,
			InputSchema: `{"type":"object","properties":{"text":{"type":"string"},"to":{"type":"string"}},"required":["text"]}`,
			Call: func(ctx context.Context, s *core.Session, args []byte) core.ToolResult {
				var in struct {
					Text *string `json:"text"`
					To   *string `json:"to"`
				}
				if !parse(args, &in) {
					return fail(badArguments)
				}
				text := ""
				if in.Text != nil {
					text = *in.Text
				}
				var to []string
				if in.To != nil {
					to = recipients(*in.To)
				}
				if err := chat.Send(ctx, s.Name, to, text); err != nil {
					return fail(err.Error())
				}
				return ok(sent)
			},
		},
		{
			Name:        "irc_read",
			Description: "Unread private messages and the last n room lines.",
			InputSchema: `{"type":"object","properties":{"n":{"type":"integer"}}}`,
			Call: func(ctx context.Context, s *core.Session, args []byte) core.ToolResult {
				var in struct {
					N *int `json:"n"`
				}
				if !parse(args, &in) {
					return fail(badArguments)
				}
				n := 0
				if in.N != nil {
					n = *in.N
				}
				msgs, err := chat.Read(ctx, s.Name, n)
				if err != nil {
					return fail(err.Error())
				}
				if len(msgs) == 0 {
					return ok(nothingNew)
				}
				lines := make([]string, 0, len(msgs))
				for _, m := range msgs {
					lines = append(lines, m.Line())
				}
				return ok(strings.Join(lines, "\n"))
			},
		},
		{
			Name:        "irc_who",
			Description: "Names seen recently.",
			InputSchema: `{"type":"object"}`,
			Call: func(ctx context.Context, _ *core.Session, args []byte) core.ToolResult {
				var in struct{}
				if !parse(args, &in) {
					return fail(badArguments)
				}
				names, err := chat.Who(ctx)
				if err != nil {
					return fail(err.Error())
				}
				if len(names) == 0 {
					return ok(nobodyYet)
				}
				return ok(strings.Join(names, " "))
			},
		},
	}
}

// parse decodes args into v. Empty args or null mean {}; anything that is not a JSON object, or a field of the
// wrong type, is refused. Unknown fields are ignored.
func parse(args []byte, v any) bool {
	trimmed := bytes.TrimSpace(args)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		return true
	}
	if trimmed[0] != '{' {
		return false
	}
	return json.Unmarshal(trimmed, v) == nil
}

// recipients splits to on commas and whitespace and drops empty parts; nil means the room.
func recipients(to string) []string {
	parts := strings.FieldsFunc(to, func(r rune) bool {
		return r == ',' || r == ' ' || r == '\t' || r == '\n' || r == '\r'
	})
	if len(parts) == 0 {
		return nil
	}
	return parts
}

func ok(text string) core.ToolResult { return core.ToolResult{Text: text} }

func fail(text string) core.ToolResult {
	if text == "" {
		text = "error"
	}
	return core.ToolResult{Text: text, IsError: true}
}
