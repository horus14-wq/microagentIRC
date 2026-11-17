package chat

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/horus14-wq/microagentIRC/internal/core"
	"github.com/horus14-wq/microagentIRC/internal/names"
)

// ---- fakes ----

type fakeClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeClock) add(d time.Duration) {
	c.mu.Lock()
	c.now = c.now.Add(d)
	c.mu.Unlock()
}

// fakeStore is an in-memory core.Store that counts every call.
type fakeStore struct {
	mu       sync.Mutex
	calls    atomic.Int64
	touches  atomic.Int64
	nextID   int64
	agents   map[string]*core.Agent
	room     []core.Message
	pms      []core.Message
	unread   map[string][]int64 // recipient -> undelivered pm ids, oldest first
	pruneLog int
}

func newFakeStore() *fakeStore {
	return &fakeStore{agents: map[string]*core.Agent{}, unread: map[string][]int64{}}
}

func (f *fakeStore) hit() { f.calls.Add(1) }

func (f *fakeStore) UpsertAgent(_ context.Context, name, seat string, now time.Time) error {
	f.hit()
	f.mu.Lock()
	defer f.mu.Unlock()
	if a, ok := f.agents[name]; ok {
		if a.Seat != "" && seat != "" && a.Seat != seat {
			return core.ErrNameTaken
		}
		a.Seat, a.LastSeen = seat, now
		return nil
	}
	f.agents[name] = &core.Agent{Name: name, Seat: seat, LastSeen: now}
	return nil
}

func (f *fakeStore) Agent(_ context.Context, name string) (core.Agent, bool, error) {
	f.hit()
	f.mu.Lock()
	defer f.mu.Unlock()
	if a, ok := f.agents[name]; ok {
		return *a, true, nil
	}
	return core.Agent{}, false, nil
}

func (f *fakeStore) AgentBySeat(_ context.Context, seat string) (core.Agent, bool, error) {
	f.hit()
	f.mu.Lock()
	defer f.mu.Unlock()
	var best *core.Agent
	for _, a := range f.agents {
		if a.Seat == seat && (best == nil || a.LastSeen.After(best.LastSeen)) {
			best = a
		}
	}
	if best == nil {
		return core.Agent{}, false, nil
	}
	return *best, true, nil
}

func (f *fakeStore) Agents(_ context.Context, since time.Time, limit int) ([]core.Agent, error) {
	f.hit()
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []core.Agent
	for _, a := range f.agents {
		if !a.LastSeen.Before(since) {
			out = append(out, *a)
		}
	}
	slices.SortFunc(out, func(a, b core.Agent) int { return b.LastSeen.Compare(a.LastSeen) })
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

func (f *fakeStore) Touch(_ context.Context, name string, now time.Time) error {
	f.hit()
	f.touches.Add(1)
	f.mu.Lock()
	defer f.mu.Unlock()
	if a, ok := f.agents[name]; ok {
		a.LastSeen = now
	}
	return nil
}

func (f *fakeStore) InsertRoom(_ context.Context, sender, body string, now time.Time) (core.Message, error) {
	f.hit()
	f.mu.Lock()
	defer f.mu.Unlock()
	f.nextID++
	m := core.Message{ID: f.nextID, At: now, Sender: sender, Kind: core.KindRoom, Body: body}
	f.room = append(f.room, m)
	if a, ok := f.agents[sender]; ok {
		a.LastPublicAt = now
	}
	return m, nil
}

func (f *fakeStore) InsertPM(_ context.Context, sender string, to []string, body string, now time.Time) (core.Message, error) {
	f.hit()
	f.mu.Lock()
	defer f.mu.Unlock()
	f.nextID++
	m := core.Message{ID: f.nextID, At: now, Sender: sender, Kind: core.KindPM, Body: body}
	f.pms = append(f.pms, m)
	for _, r := range to {
		f.unread[r] = append(f.unread[r], m.ID)
	}
	if a, ok := f.agents[sender]; ok {
		a.LastPrivateAt = now
	}
	return m, nil
}

func (f *fakeStore) UnreadPMs(_ context.Context, recipient string, _ time.Time) ([]core.Message, error) {
	f.hit()
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []core.Message
	for _, id := range f.unread[recipient] {
		for _, m := range f.pms {
			if m.ID == id {
				out = append(out, m)
			}
		}
	}
	delete(f.unread, recipient)
	return out, nil
}

func (f *fakeStore) HasUnreadPM(_ context.Context, recipient string) (bool, error) {
	f.hit()
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.unread[recipient]) > 0, nil
}

func (f *fakeStore) RoomTail(_ context.Context, limit int) ([]core.Message, error) {
	f.hit()
	f.mu.Lock()
	defer f.mu.Unlock()
	start := max(0, len(f.room)-limit)
	return slices.Clone(f.room[start:]), nil
}

func (f *fakeStore) RoomSince(_ context.Context, after int64, exclude string, limit int) ([]core.Message, error) {
	f.hit()
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []core.Message
	for _, m := range f.room {
		if m.ID > after && m.Sender != exclude {
			out = append(out, m)
		}
	}
	if len(out) > limit {
		out = out[len(out)-limit:]
	}
	return out, nil
}

func (f *fakeStore) SetRoomCursor(_ context.Context, name string, id int64) error {
	f.hit()
	f.mu.Lock()
	defer f.mu.Unlock()
	if a, ok := f.agents[name]; ok {
		a.RoomCursor = id
	}
	return nil
}

func (f *fakeStore) MaxRoomID(context.Context) (int64, error) {
	f.hit()
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.room) == 0 {
		return 0, nil
	}
	return f.room[len(f.room)-1].ID, nil
}

func (f *fakeStore) Prune(context.Context, time.Time, int, int) (int, error) {
	f.hit()
	f.mu.Lock()
	f.pruneLog++
	f.mu.Unlock()
	return 0, nil
}

func (f *fakeStore) Close() error { return nil }

// ---- helpers ----

var ctx = context.Background()

func seqRand() func(int) int {
	var i int
	return func(n int) int { i++; return i % n }
}

func setup(t testing.TB, set core.Settings, names ...string) (*Service, *fakeStore, *fakeClock) {
	t.Helper()
	st := newFakeStore()
	clk := &fakeClock{now: time.Date(2026, 11, 17, 9, 0, 0, 0, time.UTC)}
	for i, n := range names {
		if err := st.UpsertAgent(ctx, n, fmt.Sprintf("seat%d", i), clk.Now()); err != nil {
			t.Fatal(err)
		}
	}
	return New(st, clk, set, seqRand()), st, clk
}

func noCooldown() core.Settings {
	s := core.DefaultSettings()
	s.PrivateCooldownS, s.PublicCooldownS = 0, 0
	return s
}

func bodies(ms []core.Message) []string {
	out := make([]string, 0, len(ms))
	for _, m := range ms {
		out = append(out, m.Line())
	}
	return out
}

// ---- Send ----

func TestSendRulesInOrderWithExactTexts(t *testing.T) {
	long := strings.Repeat("x", 300)
	many := make([]string, 17)
	for i := range many {
		many[i] = fmt.Sprintf("a%02d", i)
	}
	off := noCooldown()
	off.PublicEnabled, off.PrivateEnabled = false, false
	cases := []struct {
		name string
		set  core.Settings
		from string
		to   []string
		text string
		want string
		is   error
	}{
		{"no name", noCooldown(), "", nil, "hi", "no name: call irc_name", core.ErrNoName},
		{"empty", noCooldown(), "alice", nil, " \n\t ", "empty", core.ErrEmpty},
		{"room off", off, "alice", nil, "hi", "room off", core.ErrRoomOff},
		{"pm off", off, "alice", []string{"bob"}, "hi", "pm off", core.ErrPMOff},
		{"empty before off", off, "alice", nil, "", "empty", core.ErrEmpty},
		{"too long", noCooldown(), "alice", nil, long, "too long 300/256", core.ErrTooLong},
		{"too many", noCooldown(), "alice", many, "hi", "too many recipients (max 16)", core.ErrTooManyRecipients},
		{"unknown", noCooldown(), "alice", []string{"bob", "dave", "carol"}, "hi", "unknown: dave,carol", core.ErrUnknown},
		{"self is unknown", noCooldown(), "alice", []string{"alice"}, "hi", "unknown: alice", core.ErrUnknown},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s, _, _ := setup(t, c.set, "alice", "bob")
			err := s.Send(ctx, c.from, c.to, c.text)
			if err == nil || err.Error() != c.want || !errors.Is(err, c.is) {
				t.Fatalf("got %v, want %q (is %v)", err, c.want, c.is)
			}
		})
	}
}

func TestSendUnknownNamesInOrderGiven(t *testing.T) {
	s, _, _ := setup(t, noCooldown(), "alice")
	err := s.Send(ctx, "alice", []string{"bob", "carol"}, "hi")
	if err == nil || err.Error() != "unknown: bob,carol" || !errors.Is(err, core.ErrUnknown) {
		t.Fatalf("got %v", err)
	}
}

func TestSendCleansAndCountsRunes(t *testing.T) {
	s, st, _ := setup(t, noCooldown(), "alice")
	if err := s.Send(ctx, "alice", nil, "a\n\tb  "); err != nil {
		t.Fatal(err)
	}
	if got := st.room[0].Body; got != "a b" {
		t.Fatalf("stored %q, want %q", got, "a b")
	}
	if err := s.Send(ctx, "alice", nil, strings.Repeat("é", 256)); err != nil {
		t.Fatalf("256 multi-byte runes refused: %v", err)
	}
	err := s.Send(ctx, "alice", nil, strings.Repeat("é", 257))
	if err == nil || err.Error() != "too long 257/256" {
		t.Fatalf("257 runes: %v", err)
	}
}

func TestSendCooldown(t *testing.T) {
	s, st, clk := setup(t, core.DefaultSettings(), "alice", "bob")
	if err := s.Send(ctx, "alice", nil, "one"); err != nil {
		t.Fatal(err)
	}
	clk.add(18 * time.Second)
	if err := s.Send(ctx, "alice", nil, "two"); err == nil || err.Error() != "cooldown 42s" || !errors.Is(err, core.ErrCooldown) {
		t.Fatalf("at 18s: %v", err)
	}
	clk.add(41 * time.Second) // 59 s after the first send
	if err := s.Send(ctx, "alice", nil, "two"); err == nil || err.Error() != "cooldown 1s" {
		t.Fatalf("at 59s: %v", err)
	}
	// Room and private cooldowns are independent; one multi-recipient message is one send.
	if err := s.Send(ctx, "alice", []string{"bob"}, "pm"); err != nil {
		t.Fatalf("pm under a room cooldown: %v", err)
	}
	if err := st.UpsertAgent(ctx, "carol", "seatc", clk.Now()); err != nil {
		t.Fatal(err)
	}
	if err := s.Send(ctx, "alice", []string{"carol"}, "pm2"); err == nil || !errors.Is(err, core.ErrCooldown) {
		t.Fatalf("second pm: %v", err)
	}
	clk.add(time.Second) // 60 s after the first room send
	if err := s.Send(ctx, "alice", nil, "two"); err != nil {
		t.Fatalf("at 60s: %v", err)
	}
}

func TestSendMultiRecipientIsOneSend(t *testing.T) {
	s, st, _ := setup(t, core.DefaultSettings(), "alice", "bob", "carol")
	if err := s.Send(ctx, "alice", []string{"bob", "Carol", "bob"}, "hi"); err != nil {
		t.Fatal(err)
	}
	if len(st.pms) != 1 || len(st.unread["bob"]) != 1 || len(st.unread["carol"]) != 1 {
		t.Fatalf("pms %d bob %v carol %v", len(st.pms), st.unread["bob"], st.unread["carol"])
	}
	if st.pruneLog != 1 {
		t.Fatalf("prune ran %d times", st.pruneLog)
	}
}

func TestZeroCooldownNeverRefuses(t *testing.T) {
	s, _, _ := setup(t, noCooldown(), "alice")
	for i := range 5 {
		if err := s.Send(ctx, "alice", nil, fmt.Sprint(i)); err != nil {
			t.Fatal(err)
		}
	}
}

// ---- SetName ----

func TestSetName(t *testing.T) {
	s, st, _ := setup(t, noCooldown())
	got, err := s.SetName(ctx, "", "seatA")
	if err != nil || !names.Valid(got) {
		t.Fatalf("generate: %q %v", got, err)
	}
	again, err := s.SetName(ctx, "", "seatA")
	if err != nil || again != got {
		t.Fatalf("repeat: %q %v, want %q", again, err, got)
	}
	if _, err := s.SetName(ctx, "9bad", "seatA"); !errors.Is(err, core.ErrNameInvalid) {
		t.Fatalf("invalid: %v", err)
	}
	if _, err := s.SetName(ctx, "room", "seatA"); !errors.Is(err, core.ErrNameInvalid) {
		t.Fatalf("reserved: %v", err)
	}
	if err := st.UpsertAgent(ctx, "bob", "seatB", time.Now()); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SetName(ctx, "Bob", "seatA"); !errors.Is(err, core.ErrNameTaken) {
		t.Fatalf("taken: %v", err)
	}
	name, err := s.SetName(ctx, "  Alice ", "seatA")
	if err != nil || name != "alice" {
		t.Fatalf("rename: %q %v", name, err)
	}
	if name, err := s.SetName(ctx, "alice", "seatA"); err != nil || name != "alice" {
		t.Fatalf("same name again: %q %v", name, err)
	}
}

// ---- Read ----

func TestReadPrivateFirstThenRoomAndMovesCursor(t *testing.T) {
	s, st, _ := setup(t, noCooldown(), "alice", "bob")
	for i := range 12 {
		if err := s.Send(ctx, "bob", nil, fmt.Sprint("r", i)); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.Send(ctx, "bob", []string{"alice"}, "secret"); err != nil {
		t.Fatal(err)
	}
	got, err := s.Read(ctx, "alice", 99)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 11 || got[0].Line() != "pm bob: secret" || got[1].Body != "r2" || got[10].Body != "r11" {
		t.Fatalf("read %v", bodies(got))
	}
	if st.agents["alice"].RoomCursor != 12 {
		t.Fatalf("cursor %d", st.agents["alice"].RoomCursor)
	}
	got, _ = s.Read(ctx, "alice", 2)
	if len(got) != 2 || got[0].Kind != core.KindRoom {
		t.Fatalf("second read %v", bodies(got))
	}
	if _, err := s.Read(ctx, "", 1); !errors.Is(err, core.ErrNoName) {
		t.Fatalf("no name: %v", err)
	}
}

// ---- Who ----

func TestWhoRecentFirst(t *testing.T) {
	s, st, clk := setup(t, noCooldown(), "alice", "bob")
	clk.add(time.Minute)
	_ = st.Touch(ctx, "bob", clk.Now())
	got, err := s.Who(ctx)
	if err != nil || !slices.Equal(got, []string{"bob", "alice"}) {
		t.Fatalf("who %v %v", got, err)
	}
}

// ---- HookEvent ----

func TestHookDefaultsPrivateNextEventRoomEveryTenth(t *testing.T) {
	s, _, _ := setup(t, noCooldown(), "alice", "bob")
	if err := s.Send(ctx, "bob", []string{"alice"}, "pm1"); err != nil {
		t.Fatal(err)
	}
	if err := s.Send(ctx, "bob", nil, "room1"); err != nil {
		t.Fatal(err)
	}
	got, _ := s.HookEvent(ctx, "alice")
	if !slices.Equal(bodies(got), []string{"pm bob: pm1"}) {
		t.Fatalf("event 1: %v", bodies(got))
	}
	for i := 2; i <= 9; i++ {
		if got, _ := s.HookEvent(ctx, "alice"); got != nil {
			t.Fatalf("event %d delivered %v", i, bodies(got))
		}
	}
	got, _ = s.HookEvent(ctx, "alice")
	if !slices.Equal(bodies(got), []string{"room bob: room1"}) {
		t.Fatalf("event 10: %v", bodies(got))
	}
	for i := range 20 {
		if got, _ := s.HookEvent(ctx, "alice"); got != nil {
			t.Fatalf("redelivered at %d: %v", i, bodies(got))
		}
	}
}

func TestHookCheckPrivateEveryThree(t *testing.T) {
	set := noCooldown()
	set.CheckPrivateEvery = 3
	s, _, _ := setup(t, set, "alice", "bob")
	if err := s.Send(ctx, "bob", []string{"alice"}, "pm1"); err != nil {
		t.Fatal(err)
	}
	for i := 1; i <= 2; i++ {
		if got, _ := s.HookEvent(ctx, "alice"); got != nil {
			t.Fatalf("event %d: %v", i, bodies(got))
		}
	}
	if got, _ := s.HookEvent(ctx, "alice"); len(got) != 1 {
		t.Fatalf("event 3: %v", bodies(got))
	}
}

func TestHookNeverDeliversOwnRoomLines(t *testing.T) {
	set := noCooldown()
	set.CheckRoomEvery = 1
	s, _, _ := setup(t, set, "alice")
	if err := s.Send(ctx, "alice", nil, "mine"); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.HookEvent(ctx, "alice"); got != nil {
		t.Fatalf("own line delivered: %v", bodies(got))
	}
}

func TestReadAfterHookShowsRoomAsHistoryAndNoPrivate(t *testing.T) {
	set := noCooldown()
	set.CheckRoomEvery = 1
	s, _, _ := setup(t, set, "alice", "bob")
	_ = s.Send(ctx, "bob", []string{"alice"}, "pm1")
	_ = s.Send(ctx, "bob", nil, "room1")
	if got, _ := s.HookEvent(ctx, "alice"); len(got) != 2 {
		t.Fatalf("hook: %v", bodies(got))
	}
	got, _ := s.Read(ctx, "alice", 0)
	if !slices.Equal(bodies(got), []string{"room bob: room1"}) {
		t.Fatalf("read after hook: %v", bodies(got))
	}
}

func TestReadClearsHookStateSoNothingIsRedelivered(t *testing.T) {
	set := noCooldown()
	set.CheckRoomEvery = 1
	s, _, _ := setup(t, set, "alice", "bob")
	_, _ = s.HookEvent(ctx, "alice") // load alice's state
	_ = s.Send(ctx, "bob", []string{"alice"}, "pm1")
	_ = s.Send(ctx, "bob", nil, "room1")
	if _, err := s.Read(ctx, "alice", 0); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.HookEvent(ctx, "alice"); got != nil {
		t.Fatalf("redelivered after read: %v", bodies(got))
	}
}

func TestHookUnnamedGetsNothing(t *testing.T) {
	s, st, _ := setup(t, noCooldown())
	before := st.calls.Load()
	if got, err := s.HookEvent(ctx, ""); got != nil || err != nil {
		t.Fatalf("%v %v", got, err)
	}
	if st.calls.Load() != before {
		t.Fatal("unnamed hook touched the store")
	}
}

func TestHookZeroCostWhenNothingIsDue(t *testing.T) {
	s, st, clk := setup(t, core.DefaultSettings(), "alice")
	_, _ = s.HookEvent(ctx, "alice") // first sight loads state and touches
	before := st.calls.Load()
	touches := st.touches.Load()
	for i := range 1000 {
		if got, err := s.HookEvent(ctx, "alice"); got != nil || err != nil {
			t.Fatalf("event %d: %v %v", i, got, err)
		}
		if i%100 == 0 {
			clk.add(30 * time.Second) // 1,000 events over ~5 minutes
		}
	}
	extra := st.calls.Load() - before
	newTouches := st.touches.Load() - touches
	if extra != newTouches || newTouches > 5 {
		t.Fatalf("store calls %d (touches %d): only once-a-minute touches allowed", extra, newTouches)
	}
}

// ---- Reload ----

func TestReloadChangesLimitsImmediately(t *testing.T) {
	s, _, _ := setup(t, noCooldown(), "alice")
	set := noCooldown()
	set.MaxPublicLen = 3
	s.Reload(set)
	if err := s.Send(ctx, "alice", nil, "four"); err == nil || err.Error() != "too long 4/3" {
		t.Fatalf("after reload: %v", err)
	}
}

// ---- concurrency ----

func TestConcurrentSendReadHook(t *testing.T) {
	s, _, _ := setup(t, noCooldown(), "a0", "a1", "a2", "a3", "a4", "a5", "a6", "a7")
	var wg sync.WaitGroup
	for g := range 8 {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			me := fmt.Sprintf("a%d", g)
			peer := fmt.Sprintf("a%d", (g+1)%8)
			for i := range 200 {
				switch i % 4 {
				case 0:
					_ = s.Send(ctx, me, nil, "hi")
				case 1:
					_ = s.Send(ctx, me, []string{peer}, "pm")
				case 2:
					_, _ = s.Read(ctx, me, 3)
				default:
					_, _ = s.HookEvent(ctx, me)
				}
			}
		}(g)
	}
	wg.Wait()
}

func BenchmarkHookEventEmpty(b *testing.B) {
	s, _, _ := setup(b, core.DefaultSettings(), "alice")
	_, _ = s.HookEvent(ctx, "alice")
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		_, _ = s.HookEvent(ctx, "alice")
	}
}
