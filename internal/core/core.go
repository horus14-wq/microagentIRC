// Package core is the contract between the packages of microagentIRC: the shared types and the interfaces each
// package implements or consumes. Packages depend on core and on the standard library, never on each other's
// concrete types, so they can be built and tested in parallel against fakes.
//
// Change this file only together with plan.md, and say so in the commit message.
package core

import (
	"context"
	"errors"
	"time"
)

// Settings are the daemon's settings (plan.md §4). Field names in JSON are the snake_case keys of that table.
type Settings struct {
	MaxPrivateLen     int    `json:"max_private_len"`
	MaxPublicLen      int    `json:"max_public_len"`
	MaxRoomLines      int    `json:"max_room_lines"`
	PrivateEnabled    bool   `json:"private_enabled"`
	PublicEnabled     bool   `json:"public_enabled"`
	PrivateCooldownS  int    `json:"private_cooldown_s"`
	PublicCooldownS   int    `json:"public_cooldown_s"`
	KeepMessages      int    `json:"keep_messages"`
	KeepHours         int    `json:"keep_hours"`
	CheckPrivateEvery int    `json:"check_private_every"`
	CheckRoomEvery    int    `json:"check_room_every"`
	Listen            string `json:"listen"`
	HookTimeoutMS     int    `json:"hook_timeout_ms"`
}

// DefaultSettings returns the defaults of plan.md §4.
func DefaultSettings() Settings {
	return Settings{
		MaxPrivateLen:     256,
		MaxPublicLen:      256,
		MaxRoomLines:      10,
		PrivateEnabled:    true,
		PublicEnabled:     true,
		PrivateCooldownS:  60,
		PublicCooldownS:   60,
		KeepMessages:      1000,
		KeepHours:         24,
		CheckPrivateEvery: 1,
		CheckRoomEvery:    10,
		Listen:            "127.0.0.1:4747",
		HookTimeoutMS:     300,
	}
}

// Kind says where a message went.
type Kind string

const (
	KindRoom Kind = "room"
	KindPM   Kind = "pm"
)

// Message is one stored message. ID grows with time; Body is single-line, trimmed text.
type Message struct {
	ID     int64
	At     time.Time
	Sender string
	Kind   Kind
	Body   string
}

// Line renders a message the way tools and hooks show it: "pm greenfox1: text" or "room reddog7: text".
func (m Message) Line() string {
	return string(m.Kind) + " " + m.Sender + ": " + m.Body
}

// Agent is a named participant.
type Agent struct {
	Name          string
	Seat          string // "" when the name is not bound to a seat
	LastSeen      time.Time
	LastPublicAt  time.Time // zero when it never sent to the room
	LastPrivateAt time.Time // zero when it never sent privately
	RoomCursor    int64     // id of the newest room message already delivered to it by a hook
}

// The errors an agent can see. Their text is served verbatim (plan.md §6); wrap them with detail using the
// constructors below, and test for them with errors.Is.
var (
	ErrNoName            = errors.New("no name: call irc_name")
	ErrEmpty             = errors.New("empty")
	ErrPMOff             = errors.New("pm off")
	ErrRoomOff           = errors.New("room off")
	ErrTooManyRecipients = errors.New("too many recipients (max 16)")
	ErrNameInvalid       = errors.New("bad name: use a-z, 0-9, _ or -, 2-24 chars, letter first")
	ErrNameTaken         = errors.New("name taken")
	ErrCooldown          = errors.New("cooldown")
	ErrTooLong           = errors.New("too long")
	ErrUnknown           = errors.New("unknown")
)

// MaxRecipients is the most agents one private message may name.
const MaxRecipients = 16

// Clock is the time source. Production uses the wall clock; tests use a fake.
type Clock interface {
	Now() time.Time
}

// Store is the database (implemented by internal/store). Every method is safe for concurrent use.
// A method that finds nothing returns a zero value and a nil error unless stated otherwise.
type Store interface {
	// UpsertAgent creates the agent or updates its seat and last-seen time. It returns ErrNameTaken when the name
	// exists and is bound to a different, non-empty seat.
	UpsertAgent(ctx context.Context, name, seat string, now time.Time) error
	// Agent returns the agent, or ok=false.
	Agent(ctx context.Context, name string) (a Agent, ok bool, err error)
	// AgentBySeat returns the agent bound to the seat, or ok=false.
	AgentBySeat(ctx context.Context, seat string) (a Agent, ok bool, err error)
	// Agents lists agents seen at or after since, most recently seen first, at most limit.
	Agents(ctx context.Context, since time.Time, limit int) ([]Agent, error)
	// Touch sets the agent's last-seen time.
	Touch(ctx context.Context, name string, now time.Time) error

	// InsertRoom stores a room message and sets the sender's last public send time.
	InsertRoom(ctx context.Context, sender, body string, now time.Time) (Message, error)
	// InsertPM stores one private message with one delivery row per recipient and sets the sender's last private
	// send time.
	InsertPM(ctx context.Context, sender string, recipients []string, body string, now time.Time) (Message, error)

	// UnreadPMs returns the recipient's undelivered private messages, oldest first, and marks them delivered.
	UnreadPMs(ctx context.Context, recipient string, now time.Time) ([]Message, error)
	// HasUnreadPM reports whether the recipient has an undelivered private message.
	HasUnreadPM(ctx context.Context, recipient string) (bool, error)
	// RoomTail returns the newest limit room messages, oldest first.
	RoomTail(ctx context.Context, limit int) ([]Message, error)
	// RoomSince returns the newest limit room messages with id > after that were not sent by exclude, oldest
	// first.
	RoomSince(ctx context.Context, after int64, exclude string, limit int) ([]Message, error)
	// SetRoomCursor records the newest room message id delivered to the agent by a hook.
	SetRoomCursor(ctx context.Context, name string, id int64) error
	// MaxRoomID returns the id of the newest room message, or 0.
	MaxRoomID(ctx context.Context) (int64, error)

	// Prune deletes messages older than keepHours and all but the newest keepMessages, with their delivery rows,
	// and returns how many messages it deleted.
	Prune(ctx context.Context, now time.Time, keepHours, keepMessages int) (int, error)
	Close() error
}

// Chat is the rule layer (implemented by internal/chat) that tools, the hook endpoint and the daemon call.
// Methods taking a name return ErrNoName when it is empty.
type Chat interface {
	// SetName binds a name to the seat. An empty want generates one. It returns the name in force.
	SetName(ctx context.Context, want, seat string) (string, error)
	// NameForSeat returns the name remembered for the seat, or "".
	NameForSeat(ctx context.Context, seat string) (string, error)
	// Send posts to the room when to is empty, otherwise privately to every name in to.
	Send(ctx context.Context, from string, to []string, text string) error
	// Read returns the agent's unread private messages (now read) and the last n room lines; n <= 0 means the
	// configured maximum.
	Read(ctx context.Context, name string, n int) ([]Message, error)
	// Who lists names seen within the retention window, most recent first.
	Who(ctx context.Context) ([]string, error)
	// HookEvent counts one hook event for the agent and returns what is due for delivery now, already marked
	// delivered. It returns nil when nothing is due; that path must not touch the database.
	HookEvent(ctx context.Context, name string) ([]Message, error)
	// Reload replaces the settings in force.
	Reload(s Settings)
}

// Session is what the daemon knows about one caller of a tool.
type Session struct {
	Name string // "" until the agent has a name
	Seat string
}

// ToolResult is the outcome of one tool call: the text served and whether it is an error.
type ToolResult struct {
	Text    string
	IsError bool
}

// Tool is one MCP tool (implemented by internal/tools, served by internal/mcp).
type Tool struct {
	Name        string
	Description string
	// InputSchema is the tool's JSON Schema as compact JSON.
	InputSchema string
	// Call runs the tool. args is the raw JSON object of arguments. It may change s.Name (irc_name does).
	Call func(ctx context.Context, s *Session, args []byte) ToolResult
}
