package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/horus14-wq/microagentIRC/internal/core"
)

// fakeChat records the calls it receives and answers what the test sets.
type fakeChat struct {
	err      error
	name     string
	msgs     []core.Message
	who      []string
	gotWant  string
	gotSeat  string
	gotFrom  string
	gotTo    []string
	gotText  string
	gotN     int
	sendSeen bool
}

func (f *fakeChat) SetName(_ context.Context, want, seat string) (string, error) {
	f.gotWant, f.gotSeat = want, seat
	if f.err != nil {
		return "", f.err
	}
	return f.name, nil
}

func (f *fakeChat) NameForSeat(context.Context, string) (string, error) { return f.name, nil }

func (f *fakeChat) Send(_ context.Context, from string, to []string, text string) error {
	f.sendSeen, f.gotFrom, f.gotTo, f.gotText = true, from, to, text
	return f.err
}

func (f *fakeChat) Read(_ context.Context, name string, n int) ([]core.Message, error) {
	f.gotFrom, f.gotN = name, n
	if f.err != nil {
		return nil, f.err
	}
	return f.msgs, nil
}

func (f *fakeChat) Who(context.Context) ([]string, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.who, nil
}

func (f *fakeChat) HookEvent(context.Context, string) ([]core.Message, error) { return nil, nil }
func (f *fakeChat) Reload(core.Settings)                                      {}

var ctx = context.Background()

func tool(t *testing.T, chat core.Chat, name string) core.Tool {
	t.Helper()
	for _, tl := range All(chat) {
		if tl.Name == name {
			return tl
		}
	}
	t.Fatalf("no tool %s", name)
	return core.Tool{}
}

func TestToolsVerbatimAndOrdered(t *testing.T) {
	want := []struct{ name, desc, schema string }{
		{"irc_name", "Set your chat name, or omit name to get one.", `{"type":"object","properties":{"name":{"type":"string"}}}`},
		{"irc_send", `Send to the room, or privately: to="a,b".`, `{"type":"object","properties":{"text":{"type":"string"},"to":{"type":"string"}},"required":["text"]}`},
		{"irc_read", "Unread private messages and the last n room lines.", `{"type":"object","properties":{"n":{"type":"integer"}}}`},
		{"irc_who", "Names seen recently.", `{"type":"object"}`},
	}
	got := All(&fakeChat{})
	if len(got) != len(want) {
		t.Fatalf("%d tools", len(got))
	}
	for i, w := range want {
		if got[i].Name != w.name || got[i].Description != w.desc || got[i].InputSchema != w.schema {
			t.Errorf("tool %d = %q %q %q", i, got[i].Name, got[i].Description, got[i].InputSchema)
		}
		if !json.Valid([]byte(got[i].InputSchema)) {
			t.Errorf("%s schema is not JSON", w.name)
		}
	}
}

// chatErrors are the chat-layer texts every tool passes through unchanged.
var chatErrors = []error{
	core.ErrNoName, core.ErrEmpty, core.ErrPMOff, core.ErrRoomOff, core.ErrTooManyRecipients,
	core.ErrNameInvalid, core.ErrNameTaken,
	fmt.Errorf("%w", errors.New("cooldown 42s")), errors.New("too long 300/256"), errors.New("unknown: bob,carol"),
}

func TestErrorsPassThroughVerbatim(t *testing.T) {
	for _, name := range []string{"irc_name", "irc_send", "irc_read", "irc_who"} {
		for _, e := range chatErrors {
			s := &core.Session{Name: "alice", Seat: "s1"}
			res := tool(t, &fakeChat{err: e}, name).Call(ctx, s, []byte(`{"text":"hi"}`))
			if !res.IsError || res.Text != e.Error() {
				t.Errorf("%s with %q: %+v", name, e, res)
			}
		}
	}
}

func TestBadArguments(t *testing.T) {
	cases := []struct{ tool, args string }{
		{"irc_name", `[]`},
		{"irc_name", `"x"`},
		{"irc_name", `{"name":3}`},
		{"irc_name", `{`},
		{"irc_send", `{"text":5}`},
		{"irc_send", `{"text":"hi","to":["a"]}`},
		{"irc_send", `7`},
		{"irc_read", `{"n":"3"}`},
		{"irc_read", `{"n":1.5}`},
		{"irc_read", `true`},
		{"irc_who", `[1]`},
	}
	for _, c := range cases {
		f := &fakeChat{}
		res := tool(t, f, c.tool).Call(ctx, &core.Session{Name: "alice"}, []byte(c.args))
		if !res.IsError || res.Text != "bad arguments" {
			t.Errorf("%s %s: %+v", c.tool, c.args, res)
		}
		if f.sendSeen {
			t.Errorf("%s %s reached the chat layer", c.tool, c.args)
		}
	}
}

func TestEmptyAndNullArgsMeanEmptyObjectAndUnknownFieldsAreIgnored(t *testing.T) {
	for _, args := range []string{"", "null", " ", `{"extra":1}`} {
		res := tool(t, &fakeChat{who: []string{"a"}}, "irc_who").Call(ctx, &core.Session{}, []byte(args))
		if res.IsError || res.Text != "a" {
			t.Errorf("irc_who %q: %+v", args, res)
		}
	}
}

func TestIrcName(t *testing.T) {
	f := &fakeChat{name: "greenfox1"}
	s := &core.Session{Seat: "seatA"}
	res := tool(t, f, "irc_name").Call(ctx, s, nil)
	if res.IsError || res.Text != "you are greenfox1" || s.Name != "greenfox1" || f.gotWant != "" || f.gotSeat != "seatA" {
		t.Fatalf("generate: %+v %+v %+v", res, s, f)
	}
	f.name = "alice"
	res = tool(t, f, "irc_name").Call(ctx, s, []byte(`{"name":"Alice"}`))
	if res.Text != "you are alice" || s.Name != "alice" || f.gotWant != "Alice" {
		t.Fatalf("set: %+v %+v", res, s)
	}
	f.err = core.ErrNameTaken
	res = tool(t, f, "irc_name").Call(ctx, s, []byte(`{"name":"bob"}`))
	if !res.IsError || res.Text != "name taken" || s.Name != "alice" {
		t.Fatalf("failed rename changed the session: %+v %+v", res, s)
	}
}

func TestIrcSendRecipients(t *testing.T) {
	cases := []struct {
		to   string
		want []string
	}{
		{"a,b", []string{"a", "b"}},
		{"a, b", []string{"a", "b"}},
		{"a b", []string{"a", "b"}},
		{" a ,, b ", []string{"a", "b"}},
		{"", nil},
		{" , ", nil},
	}
	for _, c := range cases {
		f := &fakeChat{}
		args, _ := json.Marshal(map[string]string{"text": "hi", "to": c.to})
		res := tool(t, f, "irc_send").Call(ctx, &core.Session{Name: "alice"}, args)
		if res.IsError || res.Text != "sent" || !slices.Equal(f.gotTo, c.want) || f.gotFrom != "alice" || f.gotText != "hi" {
			t.Errorf("to %q: %+v to=%q", c.to, res, f.gotTo)
		}
	}
	f := &fakeChat{}
	tool(t, f, "irc_send").Call(ctx, &core.Session{Name: "alice"}, []byte(`{"text":"hi"}`))
	if f.gotTo != nil {
		t.Errorf("no to: %q", f.gotTo)
	}
}

// TestIdleReadReplyIsTwoTokens pins the idle irc_read reply that CLIs without a hook pay for on every read
// (mairc-hd5). "nothing new" measured 2 tokens in o200k_base and cl100k_base (docs/compat.md, "Idle read reply");
// the standard library has no tokenizer, so the test pins the exact text and a byte and word bound around it.
func TestIdleReadReplyIsTwoTokens(t *testing.T) {
	res := tool(t, &fakeChat{}, "irc_read").Call(ctx, &core.Session{Name: "alice"}, nil)
	if res.IsError || res.Text != "nothing new" {
		t.Fatalf("idle reply %+v", res)
	}
	words := 1
	for _, c := range res.Text {
		if c == ' ' {
			words++
		}
	}
	if len(res.Text) > 16 || words > 2 {
		t.Fatalf("idle reply %q grew to %d bytes / %d words; re-measure tokens in docs/compat.md", res.Text, len(res.Text), words)
	}
}

func TestIrcReadFormatting(t *testing.T) {
	at := time.Date(2026, 11, 17, 9, 0, 0, 0, time.UTC)
	f := &fakeChat{msgs: []core.Message{
		{ID: 9, At: at, Sender: "greenfox1", Kind: core.KindPM, Body: "hello"},
		{ID: 3, At: at, Sender: "reddog7", Kind: core.KindRoom, Body: "hello"},
	}}
	res := tool(t, f, "irc_read").Call(ctx, &core.Session{Name: "alice"}, []byte(`{"n":5}`))
	if res.IsError || res.Text != "pm greenfox1: hello\nroom reddog7: hello" || f.gotN != 5 || f.gotFrom != "alice" {
		t.Fatalf("%+v n=%d", res, f.gotN)
	}
	res = tool(t, &fakeChat{}, "irc_read").Call(ctx, &core.Session{Name: "alice"}, nil)
	if res.IsError || res.Text != "nothing new" {
		t.Fatalf("empty: %+v", res)
	}
}

func TestIrcWho(t *testing.T) {
	res := tool(t, &fakeChat{who: []string{"greenfox1", "reddog7"}}, "irc_who").Call(ctx, &core.Session{}, nil)
	if res.IsError || res.Text != "greenfox1 reddog7" {
		t.Fatalf("%+v", res)
	}
	res = tool(t, &fakeChat{}, "irc_who").Call(ctx, &core.Session{}, nil)
	if res.IsError || res.Text != "nobody yet" {
		t.Fatalf("empty: %+v", res)
	}
}

func TestResultsAreBareText(t *testing.T) {
	f := &fakeChat{name: "a", who: []string{"a"}, msgs: []core.Message{{Kind: core.KindRoom, Sender: "a", Body: "b"}}}
	for _, tl := range All(f) {
		res := tl.Call(ctx, &core.Session{Name: "a"}, []byte(`{"text":"x"}`))
		if res.Text == "" || res.Text[len(res.Text)-1] == '\n' || res.Text[0] == '{' || res.Text[0] == '#' {
			t.Errorf("%s: %q", tl.Name, res.Text)
		}
	}
}

// toolsList builds the tools/list result object exactly as MCP serves it.
func toolsList(t *testing.T) []byte {
	t.Helper()
	type entry struct {
		Name        string          `json:"name"`
		Description string          `json:"description"`
		InputSchema json.RawMessage `json:"inputSchema"`
	}
	var list struct {
		Tools []entry `json:"tools"`
	}
	for _, tl := range All(&fakeChat{}) {
		list.Tools = append(list.Tools, entry{tl.Name, tl.Description, json.RawMessage(tl.InputSchema)})
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(list); err != nil {
		t.Fatal(err)
	}
	return bytes.TrimSuffix(buf.Bytes(), []byte("\n"))
}

func TestToolsListSizeAndGolden(t *testing.T) {
	got := toolsList(t)
	if len(got) > 1200 {
		t.Fatalf("tools/list is %d bytes, budget 1200", len(got))
	}
	path := filepath.Join("..", "..", "testdata", "vectors", "tools", "tools-list.json")
	if os.Getenv("MAIRC_UPDATE_GOLDEN") == "1" {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, got, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("tools/list changed (%d bytes):\n%s\nwant:\n%s", len(got), got, want)
	}
}
