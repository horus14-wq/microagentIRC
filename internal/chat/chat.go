// Package chat is the rule layer of microagentIRC: naming, sending, reading and hook delivery (core.Chat) on top of
// a core.Store.
package chat

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/horus14-wq/microagentIRC/internal/core"
	"github.com/horus14-wq/microagentIRC/internal/names"
)

// whoLimit is the most names Who returns.
const whoLimit = 50

// touchEvery is how often a hook event may record an agent as seen.
const touchEvery = time.Minute

// nameAttempts bounds the search for a free generated name.
const nameAttempts = 64

// detailError is an agent-visible error with detail: its text is served verbatim and errors.Is matches its base.
type detailError struct {
	base error
	text string
}

func (e *detailError) Error() string { return e.text }
func (e *detailError) Unwrap() error { return e.base }

// agentState is what the hot path keeps in memory for one agent.
type agentState struct {
	privateEvents int
	roomEvents    int
	hasUnreadPM   bool
	roomCursor    int64
	lastTouch     time.Time
}

// Service implements core.Chat.
type Service struct {
	store core.Store
	clock core.Clock
	rnd   func(n int) int

	mu        sync.Mutex
	settings  core.Settings
	agents    map[string]*agentState
	maxRoomID int64
}

var _ core.Chat = (*Service)(nil)

// New returns the rule layer. rnd(n) returns a number in [0, n) and feeds name generation.
func New(ctx context.Context, store core.Store, clock core.Clock, settings core.Settings, rnd func(n int) int) *Service {
	s := &Service{
		store:    store,
		clock:    clock,
		rnd:      rnd,
		settings: settings,
		agents:   make(map[string]*agentState),
	}
	// A failed read leaves 0: the next room send raises it again.
	if id, err := store.MaxRoomID(ctx); err == nil {
		s.maxRoomID = id
	}
	return s
}

// Reload replaces the settings in force. Counters keep their values.
func (s *Service) Reload(settings core.Settings) {
	s.mu.Lock()
	s.settings = settings
	s.mu.Unlock()
}

func (s *Service) current() core.Settings {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.settings
}

// clean turns every run of control characters into one space and trims surrounding whitespace.
func clean(text string) string {
	var b strings.Builder
	b.Grow(len(text))
	inRun := false
	for _, r := range text {
		if unicode.IsControl(r) {
			if !inRun {
				b.WriteByte(' ')
				inRun = true
			}
			continue
		}
		inRun = false
		b.WriteRune(r)
	}
	return strings.TrimSpace(b.String())
}

// SetName binds a name to the seat. An empty want keeps the seat's name or generates a free one.
func (s *Service) SetName(ctx context.Context, want, seat string) (string, error) {
	now := s.clock.Now()
	if want == "" {
		if a, ok, err := s.store.AgentBySeat(ctx, seat); err != nil {
			return "", err
		} else if ok {
			return a.Name, nil
		}
		taken := func(name string) (bool, error) {
			_, ok, err := s.store.Agent(ctx, name)
			return ok, err
		}
		// A name can be taken between the check and the bind; generate again then.
		for range nameAttempts {
			name, err := names.Generate(s.rnd, taken)
			if err != nil {
				return "", err
			}
			if err := s.store.UpsertAgent(ctx, name, seat, now); err != nil {
				if errors.Is(err, core.ErrNameTaken) {
					continue
				}
				return "", err
			}
			return name, nil
		}
		return "", core.ErrNameTaken
	}
	name := names.Normalize(want)
	if err := names.Check(name); err != nil {
		return "", err
	}
	if err := s.store.UpsertAgent(ctx, name, seat, now); err != nil {
		return "", err
	}
	return name, nil
}

// NameForSeat returns the name remembered for the seat, or "".
func (s *Service) NameForSeat(ctx context.Context, seat string) (string, error) {
	a, ok, err := s.store.AgentBySeat(ctx, seat)
	if err != nil || !ok {
		return "", err
	}
	return a.Name, nil
}

// Send posts to the room when to is empty, otherwise privately to every name in to.
func (s *Service) Send(ctx context.Context, from string, to []string, text string) error {
	if from == "" {
		return core.ErrNoName
	}
	body := clean(text)
	if body == "" {
		return core.ErrEmpty
	}
	set := s.current()
	room := len(to) == 0
	if room && !set.PublicEnabled {
		return core.ErrRoomOff
	}
	if !room && !set.PrivateEnabled {
		return core.ErrPMOff
	}
	limit := set.MaxPrivateLen
	if room {
		limit = set.MaxPublicLen
	}
	if n := utf8.RuneCountInString(body); n > limit {
		return &detailError{core.ErrTooLong, "too long " + strconv.Itoa(n) + "/" + strconv.Itoa(limit)}
	}
	var recipients []string
	if !room {
		seen := make(map[string]bool, len(to))
		for _, raw := range to {
			name := names.Normalize(raw)
			if !seen[name] {
				seen[name] = true
				recipients = append(recipients, name)
			}
		}
		if len(recipients) > core.MaxRecipients {
			return core.ErrTooManyRecipients
		}
		var unknown []string
		for _, name := range recipients {
			if name == from {
				unknown = append(unknown, name)
				continue
			}
			if _, ok, err := s.store.Agent(ctx, name); err != nil {
				return err
			} else if !ok {
				unknown = append(unknown, name)
			}
		}
		if len(unknown) > 0 {
			return &detailError{core.ErrUnknown, "unknown: " + strings.Join(unknown, ",")}
		}
	}
	now := s.clock.Now()
	if err := s.checkCooldown(ctx, from, recipients, set, now); err != nil {
		return err
	}
	var msg core.Message
	var err error
	if room {
		msg, err = s.store.InsertRoom(ctx, from, body, now)
	} else {
		msg, err = s.store.InsertPM(ctx, from, recipients, body, now)
	}
	if err != nil {
		return err
	}
	// Pruning is housekeeping (the daemon also prunes every minute): a failure must not report a stored message as
	// failed (a retry would duplicate it) nor skip the hook state below.
	_, _ = s.store.Prune(ctx, now, set.KeepHours, set.KeepMessages)
	s.mu.Lock()
	if room {
		if msg.ID > s.maxRoomID {
			s.maxRoomID = msg.ID
		}
	} else {
		for _, name := range recipients {
			if st, ok := s.agents[name]; ok {
				st.hasUnreadPM = true
			}
		}
	}
	s.mu.Unlock()
	return nil
}

// checkCooldown refuses a send that comes too soon after the sender's last one of the same kind. The two kinds are
// counted apart:
//   - the room (no recipients): one message per PublicCooldownS per sender;
//   - private: one message per PrivateCooldownS per sender AND recipient. Writing to one agent does not stop the
//     sender from writing to another. A message to several recipients is refused when any of them is still on
//     cooldown, with the longest time left, because a message goes to all of its recipients or to none.
func (s *Service) checkCooldown(ctx context.Context, from string, recipients []string, set core.Settings, now time.Time) error {
	room := len(recipients) == 0
	cooldown := time.Duration(set.PrivateCooldownS) * time.Second
	if room {
		cooldown = time.Duration(set.PublicCooldownS) * time.Second
	}
	if cooldown <= 0 {
		return nil
	}
	var left time.Duration
	if room {
		a, ok, err := s.store.Agent(ctx, from)
		if err != nil || !ok {
			return err
		}
		if !a.LastPublicAt.IsZero() {
			left = cooldown - now.Sub(a.LastPublicAt)
		}
	} else {
		// One second of slack on the window: stored times are whole milliseconds.
		last, err := s.store.PrivateSince(ctx, from, now.Add(-cooldown-time.Second))
		if err != nil {
			return err
		}
		for _, name := range recipients {
			if at, ok := last[name]; ok {
				left = max(left, cooldown-now.Sub(at))
			}
		}
	}
	if left <= 0 {
		return nil
	}
	secs := int((left + time.Second - 1) / time.Second)
	if secs < 1 {
		secs = 1
	}
	return &detailError{core.ErrCooldown, "cooldown " + strconv.Itoa(secs) + "s"}
}

// Read returns the agent's unread private messages (now read), then the last n room lines.
func (s *Service) Read(ctx context.Context, name string, n int) ([]core.Message, error) {
	if name == "" {
		return nil, core.ErrNoName
	}
	set := s.current()
	if n <= 0 || n > set.MaxRoomLines {
		n = set.MaxRoomLines
	}
	now := s.clock.Now()
	// Clear the unread flag BEFORE taking the messages: a private message sent while the read runs sets it again, so
	// the next hook delivers it. Clearing it afterwards would lose that message for the hook (a spurious true only
	// costs one empty UnreadPMs).
	s.mu.Lock()
	if st := s.agents[name]; st != nil {
		st.hasUnreadPM = false
	}
	s.mu.Unlock()
	pms, err := s.store.UnreadPMs(ctx, name, now)
	if err != nil {
		return nil, err
	}
	tail, err := s.store.RoomTail(ctx, n)
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	cursor := s.maxRoomID
	if len(tail) > 0 && tail[len(tail)-1].ID > cursor {
		cursor = tail[len(tail)-1].ID
		s.maxRoomID = cursor
	}
	if st := s.agents[name]; st != nil {
		st.roomCursor = cursor
	}
	s.mu.Unlock()
	if err := s.store.SetRoomCursor(ctx, name, cursor); err != nil {
		return nil, err
	}
	return append(pms, tail...), nil
}

// Who lists names seen within the retention window, most recent first, at most 50.
func (s *Service) Who(ctx context.Context) ([]string, error) {
	set := s.current()
	since := s.clock.Now().Add(-time.Duration(set.KeepHours) * time.Hour)
	agents, err := s.store.Agents(ctx, since, whoLimit)
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, len(agents))
	for _, a := range agents {
		names = append(names, a.Name)
	}
	return names, nil
}

// HookEvent counts one hook event for the agent and returns what is due now, already marked delivered. When nothing
// is due it returns nil without touching the store (apart from recording the agent as seen at most once a minute).
func (s *Service) HookEvent(ctx context.Context, name string) ([]core.Message, error) {
	if name == "" {
		return nil, nil
	}
	now := s.clock.Now()
	s.mu.Lock()
	defer s.mu.Unlock()
	st := s.agents[name]
	if st == nil {
		var err error
		if st, err = s.load(ctx, name); err != nil {
			return nil, err
		}
		s.agents[name] = st
	}
	st.privateEvents++
	st.roomEvents++
	var out []core.Message
	if st.privateEvents >= s.settings.CheckPrivateEvery {
		st.privateEvents = 0
		if st.hasUnreadPM {
			pms, err := s.store.UnreadPMs(ctx, name, now)
			if err != nil {
				return nil, err
			}
			st.hasUnreadPM = false
			out = append(out, pms...)
		}
	}
	if st.roomEvents >= s.settings.CheckRoomEvery {
		st.roomEvents = 0
		if s.maxRoomID > st.roomCursor {
			lines, err := s.store.RoomSince(ctx, st.roomCursor, name, s.settings.MaxRoomLines)
			if err != nil {
				return nil, err
			}
			st.roomCursor = s.maxRoomID
			if err := s.store.SetRoomCursor(ctx, name, st.roomCursor); err != nil {
				return nil, err
			}
			out = append(out, lines...)
		}
	}
	if now.Sub(st.lastTouch) >= touchEvery {
		st.lastTouch = now
		if err := s.store.Touch(ctx, name, now); err != nil {
			return nil, err
		}
	}
	if len(out) == 0 {
		return nil, nil
	}
	return out, nil
}

// load reads an agent's hot-path state from the store the first time it is seen.
func (s *Service) load(ctx context.Context, name string) (*agentState, error) {
	unread, err := s.store.HasUnreadPM(ctx, name)
	if err != nil {
		return nil, err
	}
	a, _, err := s.store.Agent(ctx, name)
	if err != nil {
		return nil, err
	}
	return &agentState{hasUnreadPM: unread, roomCursor: a.RoomCursor}, nil
}
