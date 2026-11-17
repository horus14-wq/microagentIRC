// Package mcp is the MCP protocol core: JSON-RPC 2.0 and the small MCP subset mairc serves, with no transport
// in it. The daemon calls Handle for each HTTP request and the stdio shim forwards lines to it through the daemon.
//
// Everything here is standard library (encoding/json). The subset is deliberately small so the wire behaviour
// stays ours: no resources, prompts, sampling, logging, progress, cancellation or SSE.
package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"strconv"

	"github.com/horus14-wq/microagentIRC/internal/core"
)

// protocolVersion is the MCP revision this server serves. A client asking for another revision is still answered
// with this one, so the two sides never disagree about the wire.
const protocolVersion = "2025-06-18"

// The JSON-RPC error codes and their fixed texts (the bead lists them verbatim).
const (
	codeParseError     = -32700
	codeInvalidRequest = -32600
	codeMethodNotFound = -32601
	codeInvalidParams  = -32602
)

// Server answers MCP messages for a fixed set of tools.
type Server struct {
	name    string
	version string
	tools   []core.Tool
	// list is the tools/list result object, built once: it is the same bytes for every caller and the size test
	// reads it.
	list []byte
}

// New returns a server that serves tools under the given server name and version.
func New(name, version string, tools []core.Tool) *Server {
	copied := make([]core.Tool, len(tools))
	copy(copied, tools)
	return &Server{name: name, version: version, tools: copied, list: toolList(copied)}
}

// ToolListBytes returns the exact bytes of the tools/list result object.
func (s *Server) ToolListBytes() []byte {
	out := make([]byte, len(s.list))
	copy(out, s.list)
	return out
}

// ToolList builds the tools/list result object: the tools in the order they were given to New, each schema
// embedded as raw JSON.
func toolList(tools []core.Tool) []byte {
	var b bytes.Buffer
	b.WriteString(`{"tools":[`)
	for i, tool := range tools {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteString(`{"name":`)
		writeString(&b, tool.Name)
		b.WriteString(`,"description":`)
		writeString(&b, tool.Description)
		b.WriteString(`,"inputSchema":`)
		b.WriteString(tool.InputSchema)
		b.WriteByte('}')
	}
	b.WriteString(`]}`)
	return b.Bytes()
}

// Handle processes one JSON-RPC message (a single request or notification; batches are refused) for a session.
// It returns the response bytes, or nil for a notification. It never returns an error: every failure is a
// JSON-RPC error response.
func (s *Server) Handle(ctx context.Context, sess *core.Session, msg []byte) []byte {
	var raw json.RawMessage
	if err := json.Unmarshal(msg, &raw); err != nil {
		return failure(nil, false, codeParseError, "parse error")
	}
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || trimmed[0] != '{' {
		// Not an object (a batch array included): nothing readable to echo, so id is null.
		return failure(nil, false, codeInvalidRequest, "invalid request")
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(trimmed, &fields); err != nil {
		return failure(nil, false, codeParseError, "parse error")
	}
	id, hasID := readID(fields)
	version, versionOK := readString(fields["jsonrpc"])
	method, methodOK := readString(fields["method"])
	if !versionOK || version != "2.0" || !methodOK || method == "" {
		return failure(id, hasID, codeInvalidRequest, "invalid request")
	}
	if !hasID {
		// A notification: nothing is sent back, and no tool runs.
		return nil
	}
	switch method {
	case "initialize":
		return s.initialize(id)
	case "ping":
		return response(id, true, json.RawMessage(`{}`))
	case "tools/list":
		return response(id, true, json.RawMessage(s.list))
	case "tools/call":
		return s.call(ctx, sess, id, fields["params"])
	default:
		return failure(id, true, codeMethodNotFound, "method not found")
	}
}

// initialize answers the handshake, always with the revision this server serves.
func (s *Server) initialize(id json.RawMessage) []byte {
	var b bytes.Buffer
	b.WriteString(`{"protocolVersion":`)
	writeString(&b, protocolVersion)
	b.WriteString(`,"capabilities":{"tools":{}},"serverInfo":{"name":`)
	writeString(&b, s.name)
	b.WriteString(`,"version":`)
	writeString(&b, s.version)
	b.WriteString(`}}`)
	return response(id, true, json.RawMessage(b.Bytes()))
}

// call runs one tool. A tool that reports a problem is a normal result with isError true, never a JSON-RPC error.
func (s *Server) call(ctx context.Context, sess *core.Session, id, params json.RawMessage) []byte {
	var fields map[string]json.RawMessage
	if trimmed := bytes.TrimSpace(params); len(trimmed) == 0 || trimmed[0] != '{' || json.Unmarshal(trimmed, &fields) != nil {
		return failure(id, true, codeInvalidParams, "invalid params")
	}
	name, nameOK := readString(fields["name"])
	if !nameOK || name == "" {
		return failure(id, true, codeInvalidParams, "invalid params")
	}
	tool, found := s.tool(name)
	if !found {
		return failure(id, true, codeInvalidParams, "unknown tool: "+name)
	}
	args := fields["arguments"]
	if trimmed := bytes.TrimSpace(args); len(trimmed) == 0 {
		args = json.RawMessage(`{}`)
	}
	result := tool.Call(ctx, sess, args)
	var b bytes.Buffer
	b.WriteString(`{"content":[{"type":"text","text":`)
	writeString(&b, result.Text)
	b.WriteString(`}]`)
	if result.IsError {
		b.WriteString(`,"isError":true`)
	}
	b.WriteByte('}')
	return response(id, true, json.RawMessage(b.Bytes()))
}

// tool finds a tool by name.
func (s *Server) tool(name string) (core.Tool, bool) {
	for _, tool := range s.tools {
		if tool.Name == name {
			return tool, true
		}
	}
	return core.Tool{}, false
}

// readID returns the id to echo and whether the message carried one. A number or a string is kept exactly as
// received (large numbers included); anything else could not be read, so the caller echoes null.
func readID(fields map[string]json.RawMessage) (json.RawMessage, bool) {
	raw, ok := fields["id"]
	if !ok {
		return nil, false
	}
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 {
		return nil, false
	}
	switch trimmed[0] {
	case '{', '[', 'n': // an object or an array is not an id; null means no id
		return nil, false
	case '"', '-', '0', '1', '2', '3', '4', '5', '6', '7', '8', '9':
		return json.RawMessage(trimmed), true
	default:
		return nil, false
	}
}

// readString reads a JSON string field.
func readString(raw json.RawMessage) (string, bool) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) < 2 || trimmed[0] != '"' {
		return "", false
	}
	var s string
	if err := json.Unmarshal(trimmed, &s); err != nil {
		return "", false
	}
	return s, true
}

// response builds one JSON-RPC response with a result. With no id the member reads null, which is what a
// message whose id could not be read answers with.
func response(id json.RawMessage, hasID bool, result json.RawMessage) []byte {
	var b bytes.Buffer
	b.WriteString(`{"jsonrpc":"2.0","id":`)
	writeID(&b, id, hasID)
	b.WriteString(`,"result":`)
	b.Write(result)
	b.WriteByte('}')
	return b.Bytes()
}

// failure builds one JSON-RPC error response.
func failure(id json.RawMessage, hasID bool, code int, message string) []byte {
	var b bytes.Buffer
	b.WriteString(`{"jsonrpc":"2.0","id":`)
	writeID(&b, id, hasID)
	b.WriteString(`,"error":`)
	b.Write(errorObject(code, message))
	b.WriteByte('}')
	return b.Bytes()
}

// writeID writes the echoed id, or null when the message carried none that could be read.
func writeID(b *bytes.Buffer, id json.RawMessage, hasID bool) {
	if hasID && len(id) > 0 {
		b.Write(id)
		return
	}
	b.WriteString("null")
}

// errorObject builds a JSON-RPC error member.
func errorObject(code int, message string) json.RawMessage {
	var b bytes.Buffer
	b.WriteString(`{"code":`)
	b.WriteString(strconv.Itoa(code))
	b.WriteString(`,"message":`)
	writeString(&b, message)
	b.WriteByte('}')
	return json.RawMessage(b.Bytes())
}

// writeString writes a JSON string, escaping only what JSON requires so the bytes stay predictable.
func writeString(b *bytes.Buffer, s string) {
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
			if r < 0x20 {
				b.WriteString(`\u00`)
				const hex = "0123456789abcdef"
				b.WriteByte(hex[byte(r)>>4])
				b.WriteByte(hex[byte(r)&0xf])
				continue
			}
			b.WriteRune(r)
		}
	}
	b.WriteByte('"')
}
