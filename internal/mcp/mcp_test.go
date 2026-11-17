package mcp

import (
	"context"
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/horus14-wq/microagentIRC/internal/core"
)

// The fake tools the vectors and the tests describe. The schemas are the compact JSON text a tool declares, and
// their bytes are part of the tools/list vector, so they are constants here.
const (
	echoSchema = `{"additionalProperties":false,"properties":{"text":{"type":"string"}},"required":["text"],"type":"object"}`
	failSchema = `{"additionalProperties":false,"properties":{},"type":"object"}`
	echoText   = "Echo the text back to the caller."
	failText   = "Fail on purpose, as a tool result."
)

// echoArgs is what the echo tool reads from its arguments.
type echoArgs struct {
	Text string `json:"text"`
}

// echo returns its text argument unchanged.
func echo() core.Tool {
	return core.Tool{
		Name:        "echo",
		Description: echoText,
		InputSchema: echoSchema,
		Call: func(_ context.Context, _ *core.Session, args []byte) core.ToolResult {
			var in echoArgs
			if err := json.Unmarshal(args, &in); err != nil {
				return core.ToolResult{Text: "invalid arguments", IsError: true}
			}
			return core.ToolResult{Text: in.Text}
		},
	}
}

// fail reports a problem as a tool result, not as a JSON-RPC error.
func fail() core.Tool {
	return core.Tool{
		Name:        "fail",
		Description: failText,
		InputSchema: failSchema,
		Call: func(_ context.Context, _ *core.Session, _ []byte) core.ToolResult {
			return core.ToolResult{Text: "failed on purpose", IsError: true}
		},
	}
}

// server is the server the vectors describe: two fake tools under mairc 0.1.0.
func server() *Server {
	return New("mairc", "0.1.0", []core.Tool{echo(), fail()})
}

// vector is one golden case: the message handed to Handle and the response it must return. Both are raw text so
// the comparison is byte for byte, and a case whose input is not valid JSON can still be a file. A null out is a
// notification: nothing is sent back.
type vector struct {
	In  string  `json:"in"`
	Out *string `json:"out"`
}

// vectors loads every case from testdata/vectors/mcp.
func vectors(t *testing.T) map[string]vector {
	t.Helper()
	dir := filepath.Join("..", "..", "testdata", "vectors", "mcp")
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read vectors: %v", err)
	}
	cases := make(map[string]vector, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		// One directory, read through a scoped filesystem: the vector names come from that directory only.
		raw, err := fs.ReadFile(os.DirFS(dir), entry.Name())
		if err != nil {
			t.Fatalf("read %s: %v", entry.Name(), err)
		}
		var v vector
		if err := json.Unmarshal(raw, &v); err != nil {
			t.Fatalf("%s: %v", entry.Name(), err)
		}
		cases[strings.TrimSuffix(entry.Name(), ".json")] = v
	}
	return cases
}

// TestVectors runs every golden case and compares the response byte for byte.
func TestVectors(t *testing.T) {
	cases := vectors(t)
	want := []string{
		"initialize", "initialized-notification", "ping", "tools-list", "tools-call-ok",
		"tools-call-tool-error", "tools-call-unknown-tool", "tools-call-bad-params",
		"unknown-method", "parse-error", "batch-refused", "string-id",
	}
	for _, name := range want {
		if _, ok := cases[name]; !ok {
			t.Fatalf("missing vector %q", name)
		}
	}
	for name, v := range cases {
		t.Run(name, func(t *testing.T) {
			sess := &core.Session{}
			got := server().Handle(context.Background(), sess, []byte(v.In))
			if v.Out == nil {
				if got != nil {
					t.Fatalf("notification answered %q, want nothing", got)
				}
				return
			}
			if string(got) != *v.Out {
				t.Fatalf("response\n got: %s\nwant: %s", got, *v.Out)
			}
		})
	}
}

// TestToolListBytesIsTheVectorResult pins the prebuilt list against the tools/list vector, so the size test and
// the wire cannot drift apart.
func TestToolListBytesIsTheVectorResult(t *testing.T) {
	v, ok := vectors(t)["tools-list"]
	if !ok || v.Out == nil {
		t.Fatal("no tools-list vector")
	}
	var envelope struct {
		Result json.RawMessage `json:"result"`
	}
	if err := json.Unmarshal([]byte(*v.Out), &envelope); err != nil {
		t.Fatalf("tools-list vector: %v", err)
	}
	if got := string(server().ToolListBytes()); got != string(envelope.Result) {
		t.Fatalf("ToolListBytes\n got: %s\nwant: %s", got, envelope.Result)
	}
}

// TestCallPassesRawArgumentsAndTheSession proves a tool sees the call's own argument bytes and the session it was
// handed, so a tool that names the caller changes what the caller is.
func TestCallPassesRawArgumentsAndTheSession(t *testing.T) {
	var seenArgs string
	var seenSession *core.Session
	rename := core.Tool{
		Name:        "rename",
		Description: "Take a name.",
		InputSchema: `{"type":"object"}`,
		Call: func(_ context.Context, s *core.Session, args []byte) core.ToolResult {
			seenArgs = string(args)
			seenSession = s
			s.Name = "reddog7"
			return core.ToolResult{Text: "ok"}
		},
	}
	s := New("mairc", "0.1.0", []core.Tool{rename})
	sess := &core.Session{Seat: "seat-1"}
	msg := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"rename","arguments":{"name":"reddog7","extra":[1,2]}}}`
	if got := s.Handle(context.Background(), sess, []byte(msg)); got == nil {
		t.Fatal("a request must answer")
	}
	if want := `{"name":"reddog7","extra":[1,2]}`; seenArgs != want {
		t.Fatalf("tool saw %q, want %q", seenArgs, want)
	}
	if seenSession != sess {
		t.Fatal("the tool did not get the session it was called with")
	}
	if sess.Name != "reddog7" {
		t.Fatalf("session name %q, want the name the tool set", sess.Name)
	}
}

// TestMissingArgumentsMeansEmptyObject: a tools/call without arguments calls the tool with {}.
func TestMissingArgumentsMeansEmptyObject(t *testing.T) {
	var seen string
	probe := core.Tool{
		Name:        "probe",
		Description: "Report its arguments.",
		InputSchema: `{"type":"object"}`,
		Call: func(_ context.Context, _ *core.Session, args []byte) core.ToolResult {
			seen = string(args)
			return core.ToolResult{Text: "ok"}
		},
	}
	msg := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"probe"}}`
	if got := New("mairc", "0.1.0", []core.Tool{probe}).Handle(context.Background(), &core.Session{}, []byte(msg)); got == nil {
		t.Fatal("a request must answer")
	}
	if seen != "{}" {
		t.Fatalf("tool saw %q, want {}", seen)
	}
}

// TestInitializeAnswersThisRevision: a client asking for another revision is still answered with 2025-06-18, so
// the two sides never disagree about the wire.
func TestInitializeAnswersThisRevision(t *testing.T) {
	for _, asked := range []string{"2024-11-05", "2030-01-01", ""} {
		msg := `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"` + asked + `"}}`
		want := `{"jsonrpc":"2.0","id":1,"result":{"protocolVersion":"2025-06-18","capabilities":{"tools":{}},"serverInfo":{"name":"mairc","version":"0.1.0"}}}`
		if got := string(server().Handle(context.Background(), &core.Session{}, []byte(msg))); got != want {
			t.Fatalf("asked %q\n got: %s\nwant: %s", asked, got, want)
		}
	}
}

// TestCallWithoutANameIsInvalidParams: a tools/call naming no tool is invalid params, like a params that is not an
// object.
func TestCallWithoutANameIsInvalidParams(t *testing.T) {
	want := `{"jsonrpc":"2.0","id":10,"error":{"code":-32602,"message":"invalid params"}}`
	for _, msg := range []string{
		`{"jsonrpc":"2.0","id":10,"method":"tools/call","params":{"arguments":{}}}`,
		`{"jsonrpc":"2.0","id":10,"method":"tools/call","params":{"name":"","arguments":{}}}`,
		`{"jsonrpc":"2.0","id":10,"method":"tools/call"}`,
	} {
		if got := string(server().Handle(context.Background(), &core.Session{}, []byte(msg))); got != want {
			t.Fatalf("%s\n got: %s\nwant: %s", msg, got, want)
		}
	}
}

// TestNotificationsAnswerNothingButRequestsAlwaysDo: the shape of the answer is decided by the id.
func TestNotificationsAnswerNothingButRequestsAlwaysDo(t *testing.T) {
	notifications := []string{
		`{"jsonrpc":"2.0","method":"notifications/initialized"}`,
		`{"jsonrpc":"2.0","method":"tools/list"}`,
		`{"jsonrpc":"2.0","method":"anything/else"}`,
		`{"jsonrpc":"2.0","id":null,"method":"ping"}`,
	}
	requests := []string{
		`{"jsonrpc":"2.0","id":0,"method":"ping"}`,
		`{"jsonrpc":"2.0","id":"","method":"ping"}`,
		`{"jsonrpc":"2.0","id":123456789012345678901234567890,"method":"ping"}`,
		`{"jsonrpc":"2.0","id":1.5,"method":"ping"}`,
	}
	s := server()
	for _, msg := range notifications {
		if got := s.Handle(context.Background(), &core.Session{}, []byte(msg)); got != nil {
			t.Fatalf("%s answered %q, want nothing", msg, got)
		}
	}
	for _, msg := range requests {
		if got := s.Handle(context.Background(), &core.Session{}, []byte(msg)); got == nil {
			t.Fatalf("%s answered nothing, want a response", msg)
		}
	}
}

// TestInvalidRequestsEchoNullID: a message that cannot be read answers with a null id.
func TestInvalidRequestsEchoNullID(t *testing.T) {
	cases := map[string]string{
		`{`:                        `{"jsonrpc":"2.0","id":null,"error":{"code":-32700,"message":"parse error"}}`,
		`"just a string"`:          `{"jsonrpc":"2.0","id":null,"error":{"code":-32600,"message":"invalid request"}}`,
		`[]`:                       `{"jsonrpc":"2.0","id":null,"error":{"code":-32600,"message":"invalid request"}}`,
		`{"id":1,"method":"ping"}`: `{"jsonrpc":"2.0","id":1,"error":{"code":-32600,"message":"invalid request"}}`,
		`{"jsonrpc":"1.0","id":1,"method":"ping"}`: `{"jsonrpc":"2.0","id":1,"error":{"code":-32600,"message":"invalid request"}}`,
		`{"jsonrpc":"2.0","id":1}`:                 `{"jsonrpc":"2.0","id":1,"error":{"code":-32600,"message":"invalid request"}}`,
		`{"jsonrpc":"2.0","id":1,"method":""}`:     `{"jsonrpc":"2.0","id":1,"error":{"code":-32600,"message":"invalid request"}}`,
	}
	s := server()
	for msg, want := range cases {
		if got := string(s.Handle(context.Background(), &core.Session{}, []byte(msg))); got != want {
			t.Fatalf("%s\n got: %s\nwant: %s", msg, got, want)
		}
	}
}

// FuzzHandle: arbitrary bytes never panic and always produce valid JSON or nothing.
func FuzzHandle(f *testing.F) {
	for _, seed := range []string{
		`{"jsonrpc":"2.0","id":1,"method":"ping"}`,
		`{"jsonrpc":"2.0","id":"s","method":"tools/call","params":{"name":"echo","arguments":{"text":"x"}}}`,
		`{"jsonrpc":"2.0","method":"notifications/initialized"}`,
		`[1,2]`,
		`{`,
		"",
	} {
		f.Add([]byte(seed))
	}
	s := server()
	f.Fuzz(func(t *testing.T, msg []byte) {
		got := s.Handle(context.Background(), &core.Session{}, msg)
		if got == nil {
			return
		}
		if !json.Valid(got) {
			t.Fatalf("Handle returned invalid JSON for %q: %q", msg, got)
		}
	})
}
