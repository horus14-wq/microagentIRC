package store

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"runtime"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/horus14-wq/microagentIRC/internal/core"
)

// The store must satisfy the contract.
var _ core.Store = (*Store)(nil)

// start is the fixed instant every test counts from.
func start() time.Time { return time.UnixMilli(1_800_000_000_000) }

func memory(ctx context.Context, t *testing.T) *Store {
	t.Helper()
	s, err := OpenMemory(ctx)
	if err != nil {
		t.Fatalf("OpenMemory: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func agents(ctx context.Context, t *testing.T, s *Store, names ...string) {
	t.Helper()
	for _, name := range names {
		must(t, s.UpsertAgent(ctx, name, "", start()))
	}
}

func bodies(msgs []core.Message) []string {
	out := make([]string, len(msgs))
	for i, m := range msgs {
		out[i] = m.Body
	}
	return out
}

func equal(a, b []string) bool { return slices.Equal(a, b) }

func TestUpsertAgent(t *testing.T) {
	ctx := context.Background()
	t0 := start()
	later := t0.Add(time.Minute)
	tests := []struct {
		name     string
		steps    func(s *Store) error
		wantErr  error
		wantSeat map[string]string // agent -> seat after the steps
	}{
		{
			name:     "new name with a seat",
			steps:    func(s *Store) error { return s.UpsertAgent(ctx, "fox", "tmux:%1", t0) },
			wantSeat: map[string]string{"fox": "tmux:%1"},
		},
		{
			name:     "new name without a seat",
			steps:    func(s *Store) error { return s.UpsertAgent(ctx, "fox", "", t0) },
			wantSeat: map[string]string{"fox": ""},
		},
		{
			name: "same name, same seat: updated",
			steps: func(s *Store) error {
				_ = s.UpsertAgent(ctx, "fox", "tmux:%1", t0)
				return s.UpsertAgent(ctx, "fox", "tmux:%1", later)
			},
			wantSeat: map[string]string{"fox": "tmux:%1"},
		},
		{
			name: "name without a seat takes the seat given",
			steps: func(s *Store) error {
				_ = s.UpsertAgent(ctx, "fox", "", t0)
				return s.UpsertAgent(ctx, "fox", "tmux:%1", later)
			},
			wantSeat: map[string]string{"fox": "tmux:%1"},
		},
		{
			name: "called without a seat keeps the seat",
			steps: func(s *Store) error {
				_ = s.UpsertAgent(ctx, "fox", "tmux:%1", t0)
				return s.UpsertAgent(ctx, "fox", "", later)
			},
			wantSeat: map[string]string{"fox": "tmux:%1"},
		},
		{
			name: "name bound to another seat is taken",
			steps: func(s *Store) error {
				_ = s.UpsertAgent(ctx, "fox", "tmux:%1", t0)
				return s.UpsertAgent(ctx, "fox", "tmux:%2", later)
			},
			wantErr:  core.ErrNameTaken,
			wantSeat: map[string]string{"fox": "tmux:%1"},
		},
		{
			name: "a new name in a seat releases the seat's old name",
			steps: func(s *Store) error {
				_ = s.UpsertAgent(ctx, "fox", "tmux:%1", t0)
				return s.UpsertAgent(ctx, "dog", "tmux:%1", later)
			},
			wantSeat: map[string]string{"fox": "", "dog": "tmux:%1"},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s := memory(ctx, t)
			err := tc.steps(s)
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("error = %v, want %v", err, tc.wantErr)
			}
			for name, seat := range tc.wantSeat {
				a, ok, err := s.Agent(ctx, name)
				must(t, err)
				if !ok || a.Seat != seat {
					t.Errorf("agent %s: ok=%v seat=%q, want seat %q", name, ok, a.Seat, seat)
				}
			}
		})
	}
}

func TestAgentLookupsAndTimes(t *testing.T) {
	ctx := context.Background()
	t0 := start()
	s := memory(ctx, t)
	if _, ok, err := s.Agent(ctx, "nobody"); err != nil || ok {
		t.Fatalf("missing agent: ok=%v err=%v", ok, err)
	}
	if _, ok, err := s.AgentBySeat(ctx, ""); err != nil || ok {
		t.Fatalf("empty seat: ok=%v err=%v", ok, err)
	}
	must(t, s.UpsertAgent(ctx, "fox", "tty:/dev/pts/3", t0))
	must(t, s.UpsertAgent(ctx, "dog", "", t0.Add(time.Second)))
	must(t, s.UpsertAgent(ctx, "old", "", t0.Add(-48*time.Hour)))

	a, ok, err := s.AgentBySeat(ctx, "tty:/dev/pts/3")
	must(t, err)
	if !ok || a.Name != "fox" || !a.LastSeen.Equal(t0) {
		t.Fatalf("by seat: %+v ok=%v", a, ok)
	}
	if !a.LastPublicAt.IsZero() || !a.LastPrivateAt.IsZero() || a.RoomCursor != 0 {
		t.Fatalf("a new agent has sent nothing: %+v", a)
	}

	must(t, s.Touch(ctx, "fox", t0.Add(time.Minute)))
	listed, err := s.Agents(ctx, t0.Add(-time.Hour), 10)
	must(t, err)
	if len(listed) != 2 || listed[0].Name != "fox" || listed[1].Name != "dog" {
		t.Fatalf("agents since an hour ago, most recent first: %+v", listed)
	}
	limited, err := s.Agents(ctx, time.Time{}, 1)
	must(t, err)
	if len(limited) != 1 || limited[0].Name != "fox" {
		t.Fatalf("limit 1: %+v", limited)
	}

	if _, err := s.InsertRoom(ctx, "fox", "hello", t0.Add(2*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if _, err := s.InsertPM(ctx, "fox", []string{"dog"}, "psst", t0.Add(3*time.Minute)); err != nil {
		t.Fatal(err)
	}
	a, _, err = s.Agent(ctx, "fox")
	must(t, err)
	if !a.LastPublicAt.Equal(t0.Add(2*time.Minute)) || !a.LastPrivateAt.Equal(t0.Add(3*time.Minute)) {
		t.Fatalf("send times: %+v", a)
	}
	must(t, s.SetRoomCursor(ctx, "fox", 7))
	a, _, err = s.Agent(ctx, "fox")
	must(t, err)
	if a.RoomCursor != 7 {
		t.Fatalf("room cursor = %d, want 7", a.RoomCursor)
	}
}

func TestPrivateMessagesAreDeliveredOnce(t *testing.T) {
	ctx := context.Background()
	t0 := start()
	s := memory(ctx, t)
	agents(ctx, t, s, "fox", "dog", "cat", "owl")

	m, err := s.InsertPM(ctx, "fox", []string{"dog", "cat", "dog", "owl"}, "meet at noon", t0)
	must(t, err)
	if m.ID == 0 || m.Kind != core.KindPM || m.Sender != "fox" || m.Line() != "pm fox: meet at noon" {
		t.Fatalf("stored message: %+v", m)
	}
	var rows int
	must(t, s.db.QueryRowContext(ctx, "SELECT count(*) FROM delivery").Scan(&rows))
	if rows != 3 {
		t.Fatalf("delivery rows = %d, want 3 (a recipient named twice gets one)", rows)
	}
	if _, err := s.InsertPM(ctx, "cat", []string{"dog"}, "second", t0.Add(time.Second)); err != nil {
		t.Fatal(err)
	}

	for _, name := range []string{"dog", "cat", "owl"} {
		has, err := s.HasUnreadPM(ctx, name)
		must(t, err)
		if !has {
			t.Errorf("%s should have an unread message", name)
		}
	}
	if has, err := s.HasUnreadPM(ctx, "fox"); err != nil || has {
		t.Fatalf("the sender has nothing unread: has=%v err=%v", has, err)
	}

	got, err := s.UnreadPMs(ctx, "dog", t0.Add(time.Minute))
	must(t, err)
	if !equal(bodies(got), []string{"meet at noon", "second"}) || got[0].Sender != "fox" || !got[0].At.Equal(t0) {
		t.Fatalf("dog's unread, oldest first: %+v", got)
	}
	again, err := s.UnreadPMs(ctx, "dog", t0.Add(2*time.Minute))
	must(t, err)
	if len(again) != 0 {
		t.Fatalf("a second read returned %d messages", len(again))
	}
	if has, _ := s.HasUnreadPM(ctx, "dog"); has {
		t.Fatal("dog still has unread messages after reading")
	}
	if has, _ := s.HasUnreadPM(ctx, "cat"); !has {
		t.Fatal("dog reading must not mark cat's copy")
	}
	var deliveredMS int64
	must(t, s.db.QueryRowContext(ctx,
		"SELECT delivered_ms FROM delivery WHERE recipient = 'dog' AND message_id = ?", m.ID).Scan(&deliveredMS))
	if deliveredMS != t0.Add(time.Minute).UnixMilli() {
		t.Fatalf("delivered_ms = %d", deliveredMS)
	}
}

func TestRoomReads(t *testing.T) {
	ctx := context.Background()
	t0 := start()
	s := memory(ctx, t)
	agents(ctx, t, s, "fox", "dog")
	if id, err := s.MaxRoomID(ctx); err != nil || id != 0 {
		t.Fatalf("empty room: id=%d err=%v", id, err)
	}
	ids := make([]int64, 0, 6)
	for i := range 6 {
		sender := "fox"
		if i%2 == 1 {
			sender = "dog"
		}
		m, err := s.InsertRoom(ctx, sender, fmt.Sprintf("line %d", i), t0.Add(time.Duration(i)*time.Second))
		must(t, err)
		ids = append(ids, m.ID)
	}
	// A private message never shows in the room.
	if _, err := s.InsertPM(ctx, "fox", []string{"dog"}, "private", t0.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if id, err := s.MaxRoomID(ctx); err != nil || id != ids[5] {
		t.Fatalf("MaxRoomID = %d, want %d (err %v)", id, ids[5], err)
	}

	tail, err := s.RoomTail(ctx, 3)
	must(t, err)
	if !equal(bodies(tail), []string{"line 3", "line 4", "line 5"}) || tail[0].Line() != "room dog: line 3" {
		t.Fatalf("tail 3, oldest first: %v", bodies(tail))
	}
	all, err := s.RoomTail(ctx, 100)
	must(t, err)
	if len(all) != 6 {
		t.Fatalf("tail 100 returned %d lines", len(all))
	}

	// More new lines than the limit: the newest ones, in ascending order, without the reader's own.
	since, err := s.RoomSince(ctx, ids[0], "dog", 2)
	must(t, err)
	if !equal(bodies(since), []string{"line 2", "line 4"}) {
		t.Fatalf("since line 0, not dog's, newest 2: %v", bodies(since))
	}
	none, err := s.RoomSince(ctx, ids[5], "dog", 10)
	must(t, err)
	if len(none) != 0 {
		t.Fatalf("nothing is newer than the newest: %v", bodies(none))
	}
}

func TestPrune(t *testing.T) {
	ctx := context.Background()
	t0 := start()
	fill := func(t *testing.T, s *Store) {
		t.Helper()
		agents(ctx, t, s, "fox", "dog")
		for i := range 10 {
			when := t0.Add(time.Duration(i) * time.Hour)
			if i%2 == 0 {
				_, err := s.InsertRoom(ctx, "fox", fmt.Sprintf("m%d", i), when)
				must(t, err)
			} else {
				_, err := s.InsertPM(ctx, "fox", []string{"dog"}, fmt.Sprintf("m%d", i), when)
				must(t, err)
			}
		}
	}
	count := func(t *testing.T, s *Store, table string) int {
		t.Helper()
		var n int
		must(t, s.db.QueryRowContext(ctx, "SELECT count(*) FROM "+table).Scan(&n))
		return n
	}
	tests := []struct {
		name                      string
		now                       time.Time
		keepHours, keepMessages   int
		wantDeleted, wantMessages int
		wantDeliveries            int
	}{
		{"nothing to do", t0.Add(9 * time.Hour), 24, 1000, 0, 10, 5},
		{"by age: older than 4 hours at hour 9", t0.Add(9 * time.Hour), 4, 1000, 5, 5, 3},
		{"by count: newest 3", t0.Add(9 * time.Hour), 24, 3, 7, 3, 2},
		{"both: age leaves 5, count leaves 2", t0.Add(9 * time.Hour), 4, 2, 8, 2, 1},
		{"count 1 keeps the newest", t0.Add(9 * time.Hour), 24, 1, 9, 1, 1},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s := memory(ctx, t)
			fill(t, s)
			deleted, err := s.Prune(ctx, tc.now, tc.keepHours, tc.keepMessages)
			must(t, err)
			if deleted != tc.wantDeleted {
				t.Errorf("deleted = %d, want %d", deleted, tc.wantDeleted)
			}
			if got := count(t, s, "message"); got != tc.wantMessages {
				t.Errorf("messages left = %d, want %d", got, tc.wantMessages)
			}
			if got := count(t, s, "delivery"); got != tc.wantDeliveries {
				t.Errorf("delivery rows left = %d, want %d (they follow their message)", got, tc.wantDeliveries)
			}
		})
	}
}

func TestFileBackedStoreKeepsItsData(t *testing.T) {
	ctx := context.Background()
	t0 := start()
	// A path with characters that mean something in a URI. Windows forbids "?" in file names (TestFileURI still covers
	// its escaping there).
	name := "odd name #1 100%?.db"
	if runtime.GOOS == "windows" {
		name = "odd name #1 100%.db"
	}
	path := filepath.Join(t.TempDir(), name)
	s, err := Open(ctx, path)
	must(t, err)
	must(t, s.UpsertAgent(ctx, "fox", "cwd:/work", t0))
	_, err = s.InsertRoom(ctx, "fox", "still here", t0)
	must(t, err)
	var mode string
	must(t, s.db.QueryRowContext(ctx, "PRAGMA journal_mode").Scan(&mode))
	if mode != "wal" {
		t.Errorf("journal_mode = %q, want wal", mode)
	}
	var foreignKeys int
	must(t, s.db.QueryRowContext(ctx, "PRAGMA foreign_keys").Scan(&foreignKeys))
	if foreignKeys != 1 {
		t.Errorf("foreign_keys = %d, want 1", foreignKeys)
	}
	must(t, s.Close())

	// Opening again applies the schema a second time, harmlessly, and finds the data.
	s, err = Open(ctx, path)
	must(t, err)
	defer func() { _ = s.Close() }()
	a, ok, err := s.AgentBySeat(ctx, "cwd:/work")
	must(t, err)
	tail, err := s.RoomTail(ctx, 10)
	must(t, err)
	if !ok || a.Name != "fox" || !equal(bodies(tail), []string{"still here"}) {
		t.Fatalf("after reopening: agent %+v ok=%v, room %v", a, ok, bodies(tail))
	}
}

func TestConcurrentUse(t *testing.T) {
	ctx := context.Background()
	t0 := start()
	s := memory(ctx, t)
	const workers, each = 8, 25
	names := make([]string, workers)
	for i := range names {
		names[i] = fmt.Sprintf("agent%d", i)
	}
	agents(ctx, t, s, names...)

	var wg sync.WaitGroup
	errs := make(chan error, workers*each*4)
	read := make(chan int, workers*each)
	for w := range workers {
		wg.Go(func() {
			me, peer := names[w], names[(w+1)%workers]
			for i := range each {
				when := t0.Add(time.Duration(i) * time.Millisecond)
				if _, err := s.InsertRoom(ctx, me, fmt.Sprintf("%s room %d", me, i), when); err != nil {
					errs <- err
				}
				if _, err := s.InsertPM(ctx, me, []string{peer}, fmt.Sprintf("%s pm %d", me, i), when); err != nil {
					errs <- err
				}
				got, err := s.UnreadPMs(ctx, me, when)
				if err != nil {
					errs <- err
				}
				read <- len(got)
				if _, err := s.RoomSince(ctx, 0, me, 10); err != nil {
					errs <- err
				}
			}
		})
	}
	wg.Wait()
	close(errs)
	close(read)
	for err := range errs {
		t.Fatal(err)
	}
	delivered := 0
	for n := range read {
		delivered += n
	}
	// Whatever was not read during the run is still unread; nothing was delivered twice or lost.
	for _, name := range names {
		rest, err := s.UnreadPMs(ctx, name, t0)
		must(t, err)
		delivered += len(rest)
	}
	if delivered != workers*each {
		t.Fatalf("private messages delivered = %d, want %d: each exactly once", delivered, workers*each)
	}
	all, err := s.RoomTail(ctx, workers*each*2)
	must(t, err)
	seen := make(map[string]bool, len(all))
	for _, m := range all {
		if seen[m.Body] {
			t.Fatalf("room line stored twice: %q", m.Body)
		}
		seen[m.Body] = true
	}
	if len(all) != workers*each {
		t.Fatalf("room lines stored = %d, want %d", len(all), workers*each)
	}
}

func TestParseQueries(t *testing.T) {
	got, err := parseQueries("-- a comment before any name\n-- name: One\nSELECT 1;\n\n-- name: Two\nSELECT\n  2;\n")
	must(t, err)
	if len(got) != 2 || got["One"] != "SELECT 1;" || got["Two"] != "SELECT\n  2;" {
		t.Fatalf("parsed: %q", got)
	}
	for name, text := range map[string]string{
		"empty statement": "-- name: One\n\n-- name: Two\nSELECT 2;\n",
		"defined twice":   "-- name: One\nSELECT 1;\n-- name: One\nSELECT 2;\n",
	} {
		if _, err := parseQueries(text); err == nil {
			t.Errorf("%s: no error", name)
		}
	}
	// Every statement the code asks for exists in the embedded file.
	embedded, err := parseQueries(queriesSQL)
	must(t, err)
	for _, name := range []string{
		"AgentSeat", "ReleaseSeat", "InsertAgent", "UpdateAgent", "AgentByName", "AgentBySeat", "Agents", "Touch",
		"InsertMessage", "SetLastPublic", "SetLastPrivate", "InsertDelivery", "UnreadPMs", "MarkDelivered",
		"HasUnreadPM", "RoomTail", "RoomSince", "SetRoomCursor", "MaxRoomID", "PruneByAge", "PruneByCount",
	} {
		if embedded[name] == "" {
			t.Errorf("statement %s is missing from queries.sql", name)
		}
	}
	if len(embedded) != 21 {
		t.Errorf("queries.sql has %d statements, the code uses 21", len(embedded))
	}
}

func BenchmarkInsertRoom(b *testing.B) {
	ctx := context.Background()
	t0 := start()
	s, err := Open(ctx, filepath.Join(b.TempDir(), "bench.db"))
	if err != nil {
		b.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	if err := s.UpsertAgent(ctx, "fox", "", t0); err != nil {
		b.Fatal(err)
	}
	b.ResetTimer()
	for b.Loop() {
		if _, err := s.InsertRoom(ctx, "fox", "a typical line of chat, about fifty characters long", t0); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkHasUnreadPM(b *testing.B) {
	ctx := context.Background()
	t0 := start()
	s, err := Open(ctx, filepath.Join(b.TempDir(), "bench.db"))
	if err != nil {
		b.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	for _, name := range []string{"fox", "dog"} {
		if err := s.UpsertAgent(ctx, name, "", t0); err != nil {
			b.Fatal(err)
		}
	}
	for range 500 {
		if _, err := s.InsertPM(ctx, "fox", []string{"dog"}, "hello", t0); err != nil {
			b.Fatal(err)
		}
	}
	b.ResetTimer()
	for b.Loop() {
		if _, err := s.HasUnreadPM(ctx, "fox"); err != nil {
			b.Fatal(err)
		}
	}
}

// The URI filename for each OS: Unix paths are used as they are; Windows separators become "/" and a drive path gets
// the leading "/" SQLite needs. URI-significant characters are escaped on both.
func TestFileURI(t *testing.T) {
	cases := []struct {
		path    string
		windows bool
		want    string
	}{
		{"/home/a/mairc.db", false, "file:/home/a/mairc.db"},
		{"/tmp/odd name #1 100%?.db", false, "file:/tmp/odd name %231 100%25%3f.db"},
		{`C:\Users\a\AppData\Local\mairc\mairc.db`, true, "file:/C:/Users/a/AppData/Local/mairc/mairc.db"},
		{`C:\Temp\odd name #1 100%?.db`, true, "file:/C:/Temp/odd name %231 100%25%3f.db"},
		{`relative\mairc.db`, true, "file:relative/mairc.db"},
	}
	for _, tc := range cases {
		if got := fileURI(tc.path, tc.windows); got != tc.want {
			t.Errorf("fileURI(%q, %v) = %q, want %q", tc.path, tc.windows, got, tc.want)
		}
	}
}
