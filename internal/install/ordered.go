package install

import (
	"bytes"
	"encoding/json"
	"errors"
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

// node is an order-preserving JSON value: an object keeps its keys in file order, and a value read from a file keeps
// its own text and the whitespace around it, so content the installer does not touch is written back byte for byte.
type node struct {
	kind  nodeKind
	keys  []string // object keys, parallel to items
	items []*node  // object values or array elements
	raw   []byte   // a scalar's JSON text, as written in the file

	at       *placement // where the value sat in the file; nil for a value the installer built
	parsed   bool       // a container read from the file
	wasEmpty bool       // ... that had no members there
	close    []byte     // ... and the whitespace before its closing bracket (between the brackets when empty)
}

// A placement is the text around a value inside its parent, as read. For the document itself, lead and post are the
// whitespace before and after it.
type placement struct {
	lead []byte // whitespace before the value (before its key, in an object)
	key  []byte // an object member's key, as written
	sep  []byte // between the key and the value: the colon and the whitespace around it
	post []byte // whitespace after the value, before the comma that follows it
}

// parse decodes one JSON document from data, keeping object key order, every scalar's text and the whitespace between
// tokens, so render can write it back unchanged.
func parse(data []byte) (*node, error) {
	if !json.Valid(data) {
		return nil, errNotJSON
	}
	s := &scanner{data: data}
	lead := s.space()
	n, err := s.value()
	if err != nil {
		return nil, errNotJSON
	}
	n.at = &placement{lead: lead, post: s.space()}
	if s.pos != len(data) {
		return nil, errNotJSON
	}
	return n, nil
}

// scanner walks a document json.Valid has accepted, recording where every value and separator is.
type scanner struct {
	data []byte
	pos  int
}

func (s *scanner) peek() byte {
	if s.pos < len(s.data) {
		return s.data[s.pos]
	}
	return 0
}

// space consumes JSON whitespace and returns it.
func (s *scanner) space() []byte {
	start := s.pos
	for s.pos < len(s.data) && strings.IndexByte(" \t\r\n", s.data[s.pos]) >= 0 {
		s.pos++
	}
	return s.data[start:s.pos]
}

// expect consumes one byte, which must be c.
func (s *scanner) expect(c byte) error {
	if s.peek() != c {
		return errNotJSON
	}
	s.pos++
	return nil
}

// stringText consumes a JSON string and returns its text, quotes included.
func (s *scanner) stringText() ([]byte, error) {
	start := s.pos
	if err := s.expect('"'); err != nil {
		return nil, err
	}
	for s.pos < len(s.data) {
		switch s.data[s.pos] {
		case '\\':
			s.pos += 2
		case '"':
			s.pos++
			return s.data[start:s.pos], nil
		default:
			s.pos++
		}
	}
	return nil, errNotJSON
}

func (s *scanner) value() (*node, error) {
	switch s.peek() {
	case '{':
		return s.container(objectNode, '}')
	case '[':
		return s.container(arrayNode, ']')
	case '"':
		raw, err := s.stringText()
		return &node{kind: scalarNode, raw: raw}, err
	}
	start := s.pos
	for s.pos < len(s.data) && strings.IndexByte(" \t\r\n,]}", s.data[s.pos]) < 0 {
		s.pos++
	}
	if s.pos == start {
		return nil, errNotJSON
	}
	return &node{kind: scalarNode, raw: s.data[start:s.pos]}, nil
}

// container reads an object or array whose opening bracket is at the cursor.
func (s *scanner) container(kind nodeKind, closing byte) (*node, error) {
	s.pos++
	n := &node{kind: kind, parsed: true}
	lead := s.space()
	if s.peek() == closing {
		s.pos++
		n.wasEmpty, n.close = true, lead
		return n, nil
	}
	for {
		at := &placement{lead: lead}
		if kind == objectNode {
			keyText, err := s.stringText()
			if err != nil {
				return nil, err
			}
			var key string
			if json.Unmarshal(keyText, &key) != nil {
				return nil, errNotJSON
			}
			sepStart := s.pos
			s.space()
			if err := s.expect(':'); err != nil {
				return nil, err
			}
			s.space()
			at.key, at.sep = keyText, s.data[sepStart:s.pos]
			n.keys = append(n.keys, key)
		}
		child, err := s.value()
		if err != nil {
			return nil, err
		}
		child.at = at
		n.items = append(n.items, child)
		trailing := s.space()
		switch s.peek() {
		case ',':
			s.pos++
			at.post = trailing
			lead = s.space()
		case closing:
			s.pos++
			n.close = trailing
			return n, nil
		default:
			return nil, errNotJSON
		}
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

// bytes renders the value canonically, ignoring how the file spelled its whitespace: two-space indentation and a
// trailing newline. It is what a new file holds, and what equal compares.
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

// A style is how new lines are written into an existing document: its indent unit, line ending and key separator,
// or no line breaks at all for a compact file.
type style struct {
	newline        bool
	unit, eol, sep string
}

// styleOf reads the document's style from its first member; a new or empty document gets the canonical style of
// bytes (two spaces, "\n", ": ").
func styleOf(root *node) style {
	st := style{newline: true, unit: "  ", eol: "\n", sep: ": "}
	if len(root.items) == 0 || root.items[0].at == nil {
		return st
	}
	first := root.items[0].at
	if len(first.sep) > 0 {
		st.sep = string(first.sep)
	}
	lead := string(first.lead)
	i := strings.LastIndexByte(lead, '\n')
	if i < 0 {
		st.newline = false
		return st
	}
	if strings.Contains(lead, "\r\n") {
		st.eol = "\r\n"
	}
	if unit := lead[i+1:]; unit != "" {
		st.unit = unit
	}
	return st
}

// render writes the document as it will be stored: everything read from the file in its own text and whitespace, and
// what the installer added in the file's style. A document the installer built renders as bytes does.
func (n *node) render() []byte {
	var b bytes.Buffer
	st := styleOf(n)
	if n.at != nil {
		b.Write(n.at.lead)
	}
	n.emit(&b, 0, st)
	if n.at != nil {
		b.Write(n.at.post)
	} else {
		b.WriteString("\n")
	}
	return b.Bytes()
}

func (n *node) emit(b *bytes.Buffer, depth int, st style) {
	if n.kind == scalarNode {
		b.Write(n.raw)
		return
	}
	open, closing := byte('{'), byte('}')
	if n.kind == arrayNode {
		open, closing = '[', ']'
	}
	b.WriteByte(open)
	if len(n.items) == 0 {
		if n.parsed && n.wasEmpty {
			b.Write(n.close)
		}
		b.WriteByte(closing)
		return
	}
	for i, child := range n.items {
		if i > 0 {
			b.WriteByte(',')
		}
		at := n.placementOf(i, depth, st)
		b.Write(at.lead)
		if n.kind == objectNode {
			b.Write(at.key)
			b.Write(at.sep)
		}
		child.emit(b, depth+1, st)
		b.Write(at.post)
	}
	switch {
	case n.parsed && !n.wasEmpty:
		b.Write(n.close)
	case st.newline:
		b.WriteString(st.eol + strings.Repeat(st.unit, depth))
	}
	b.WriteByte(closing)
}

// placementOf is the text around item i: as read, or for a new item copied from the nearest sibling read from the
// file (so it lines up with them), else in the document's style.
func (n *node) placementOf(i, depth int, st style) *placement {
	if at := n.items[i].at; at != nil {
		return at
	}
	at := &placement{sep: []byte(st.sep)}
	if st.newline {
		at.lead = []byte(st.eol + strings.Repeat(st.unit, depth+1))
	}
	for _, j := range siblingOrder(i, len(n.items)) {
		if sib := n.items[j].at; sib != nil {
			at.lead, at.sep = sib.lead, sib.sep
			break
		}
	}
	if n.kind == objectNode {
		at.key, _ = encodeScalar(n.keys[i])
	}
	return at
}

// siblingOrder lists the other indexes, nearest first, earlier before later.
func siblingOrder(i, count int) []int {
	order := make([]int, 0, count)
	for d := 1; d < count; d++ {
		if i-d >= 0 {
			order = append(order, i-d)
		}
		if i+d < count {
			order = append(order, i+d)
		}
	}
	return order
}

// takePlace gives n the place old had in the file, so a replaced value sits where the old one did.
func (n *node) takePlace(old *node) *node {
	if old != nil {
		n.at = old.at
	}
	return n
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
			n.items[i] = value.takePlace(n.items[i])
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
