package install

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"strings"
)

// errNotJSON is the one parse failure the installer reports; the file is never touched after it.
var errNotJSON = errors.New("not JSON")

type nodeKind int

const (
	objectNode nodeKind = iota
	arrayNode
	scalarNode
)

// node is an order-preserving JSON value: an object keeps its keys in file order, so content the installer does not
// touch is written back in the order it was read.
type node struct {
	kind  nodeKind
	keys  []string // object keys, parallel to items
	items []*node  // object values or array elements
	raw   []byte   // a scalar's JSON text
}

// parse decodes one JSON document from data, keeping object key order and number text exactly.
func parse(data []byte) (*node, error) {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	n, err := parseValue(dec)
	if err != nil {
		return nil, errNotJSON
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return nil, errNotJSON
	}
	return n, nil
}

func parseValue(dec *json.Decoder) (*node, error) {
	tok, err := dec.Token()
	if err != nil {
		return nil, err
	}
	delim, isDelim := tok.(json.Delim)
	if !isDelim {
		raw, err := encodeScalar(tok)
		if err != nil {
			return nil, err
		}
		return &node{kind: scalarNode, raw: raw}, nil
	}
	switch delim {
	case '{':
		n := &node{kind: objectNode}
		for dec.More() {
			keyTok, err := dec.Token()
			if err != nil {
				return nil, err
			}
			key, ok := keyTok.(string)
			if !ok {
				return nil, errNotJSON
			}
			value, err := parseValue(dec)
			if err != nil {
				return nil, err
			}
			n.keys = append(n.keys, key)
			n.items = append(n.items, value)
		}
		if _, err := dec.Token(); err != nil {
			return nil, err
		}
		return n, nil
	case '[':
		n := &node{kind: arrayNode}
		for dec.More() {
			value, err := parseValue(dec)
			if err != nil {
				return nil, err
			}
			n.items = append(n.items, value)
		}
		if _, err := dec.Token(); err != nil {
			return nil, err
		}
		return n, nil
	default:
		return nil, errNotJSON
	}
}

// encodeScalar writes a string, number, boolean or null as JSON text, with HTML escaping off.
func encodeScalar(v any) ([]byte, error) {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return bytes.TrimSuffix(b.Bytes(), []byte("\n")), nil
}

// bytes renders the document with two-space indentation and a trailing newline.
func (n *node) bytes() []byte {
	var b bytes.Buffer
	n.write(&b, 0)
	b.WriteByte('\n')
	return b.Bytes()
}

func (n *node) write(b *bytes.Buffer, depth int) {
	switch n.kind {
	case scalarNode:
		b.Write(n.raw)
	case objectNode:
		if len(n.keys) == 0 {
			b.WriteString("{}")
			return
		}
		b.WriteString("{\n")
		for i, key := range n.keys {
			b.WriteString(strings.Repeat("  ", depth+1))
			k, _ := encodeScalar(key)
			b.Write(k)
			b.WriteString(": ")
			n.items[i].write(b, depth+1)
			if i < len(n.keys)-1 {
				b.WriteByte(',')
			}
			b.WriteByte('\n')
		}
		b.WriteString(strings.Repeat("  ", depth))
		b.WriteByte('}')
	case arrayNode:
		if len(n.items) == 0 {
			b.WriteString("[]")
			return
		}
		b.WriteString("[\n")
		for i, item := range n.items {
			b.WriteString(strings.Repeat("  ", depth+1))
			item.write(b, depth+1)
			if i < len(n.items)-1 {
				b.WriteByte(',')
			}
			b.WriteByte('\n')
		}
		b.WriteString(strings.Repeat("  ", depth))
		b.WriteByte(']')
	}
}

// equal reports whether two values render identically.
func (n *node) equal(other *node) bool {
	return other != nil && bytes.Equal(n.bytes(), other.bytes())
}

// get returns an object's value for key, or nil.
func (n *node) get(key string) *node {
	if n == nil || n.kind != objectNode {
		return nil
	}
	for i, k := range n.keys {
		if k == key {
			return n.items[i]
		}
	}
	return nil
}

// set replaces key's value in place, or appends the key when the object does not have it.
func (n *node) set(key string, value *node) {
	for i, k := range n.keys {
		if k == key {
			n.items[i] = value
			return
		}
	}
	n.keys = append(n.keys, key)
	n.items = append(n.items, value)
}

// del removes key and reports whether it was there.
func (n *node) del(key string) bool {
	for i, k := range n.keys {
		if k == key {
			n.keys = append(n.keys[:i], n.keys[i+1:]...)
			n.items = append(n.items[:i], n.items[i+1:]...)
			return true
		}
	}
	return false
}

// child returns key's value, creating it with kind when the key is missing. ok is false when the key holds a value
// of another kind: the installer then does not guess.
func (n *node) child(key string, kind nodeKind) (*node, bool) {
	existing := n.get(key)
	if existing == nil {
		created := &node{kind: kind}
		n.set(key, created)
		return created, true
	}
	return existing, existing.kind == kind
}

// stringValue returns a scalar string's value, or "" and false.
func (n *node) stringValue() (string, bool) {
	if n == nil || n.kind != scalarNode {
		return "", false
	}
	var s string
	if json.Unmarshal(n.raw, &s) != nil {
		return "", false
	}
	return s, true
}

func str(s string) *node {
	raw, _ := encodeScalar(s)
	return &node{kind: scalarNode, raw: raw}
}

func num(text string) *node {
	return &node{kind: scalarNode, raw: []byte(text)}
}

func object(pairs ...any) *node {
	n := &node{kind: objectNode}
	for i := 0; i+1 < len(pairs); i += 2 {
		key, _ := pairs[i].(string)
		value, _ := pairs[i+1].(*node)
		n.set(key, value)
	}
	return n
}

func array(items ...*node) *node {
	return &node{kind: arrayNode, items: items}
}
