// Package store is the SQLite database of mairc. It implements core.Store and nothing else: it stores what it is
// given. Rules (cooldowns, limits, names) live in internal/chat.
//
// All access goes through one connection, so every method is serialized. That is fast enough for a chat of a few
// dozen agents and keeps a later port simple: one connection behind a mutex.
package store

import (
	"context"
	"database/sql"
	_ "embed" // the schema and the statements are embedded .sql files
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	_ "modernc.org/sqlite" // the pure-Go SQLite driver, registered as "sqlite"

	"github.com/horus14-wq/microagentIRC/internal/core"
)

//go:embed sql/schema.sql
var schemaSQL string

//go:embed sql/queries.sql
var queriesSQL string

const nameMarker = "-- name:"

// Store is the database. It implements core.Store.
type Store struct {
	db    *sql.DB
	stmts map[string]*sql.Stmt
}

// Open opens the database file, creating it if needed, applies the schema and prepares every statement.
func Open(ctx context.Context, path string) (*Store, error) {
	if err := ensurePrivate(path); err != nil {
		return nil, err
	}
	return open(ctx, fileURI(path, runtime.GOOS == "windows")+"?_pragma=journal_mode(WAL)&_pragma=synchronous(NORMAL)"+
		"&_pragma=foreign_keys(ON)&_pragma=busy_timeout(2000)")
}

// ensurePrivate creates the database file 0600 before SQLite opens it, and tightens a database (and WAL/SHM files)
// that an earlier version left 0644. SQLite gives the -wal and -shm files it creates the main file's permissions.
func ensurePrivate(path string) error {
	f, err := os.OpenFile(filepath.Clean(path), os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return fmt.Errorf("create database: %w", err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("create database: %w", err)
	}
	for _, p := range []string{path, path + "-wal", path + "-shm"} {
		if err := os.Chmod(p, 0o600); err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("make %s private: %w", filepath.Base(p), err)
		}
	}
	return nil
}

// fileURI is path as an SQLite URI filename (sqlite.org/uri.html). Only the characters that would end or alter the
// URI are escaped. On Windows the separators become "/" and a drive path gets a leading "/" ("file:/C:/a/b.db"):
// SQLite does not read "file:C:\a\b.db" as a path, and failed to open the database there (error 14).
func fileURI(path string, windows bool) string {
	if windows {
		path = strings.ReplaceAll(path, `\`, "/")
		if len(path) >= 2 && path[1] == ':' {
			path = "/" + path
		}
	}
	return "file:" + strings.NewReplacer("%", "%25", "?", "%3f", "#", "%23").Replace(path)
}

// OpenMemory opens a private in-memory database, for tests.
func OpenMemory(ctx context.Context) (*Store, error) {
	return open(ctx, "file::memory:?_pragma=foreign_keys(ON)")
}

func open(ctx context.Context, dsn string) (*Store, error) {
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open database: %w", err)
	}
	// One connection: an in-memory database lives in its connection, and a file database gets one writer.
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	db.SetConnMaxLifetime(0)
	if _, err := db.ExecContext(ctx, schemaSQL); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("apply schema: %w", err)
	}
	queries, err := parseQueries(queriesSQL)
	if err != nil {
		_ = db.Close()
		return nil, err
	}
	s := &Store{db: db, stmts: make(map[string]*sql.Stmt, len(queries))}
	for name, text := range queries {
		stmt, err := db.PrepareContext(ctx, text) //nolint:sqlclosecheck // kept for the store's lifetime; Close closes it
		if err != nil {
			_ = s.Close()
			return nil, fmt.Errorf("prepare %s: %w", name, err)
		}
		s.stmts[name] = stmt
	}
	return s, nil
}

// parseQueries splits the statements file at its "-- name: X" markers.
func parseQueries(text string) (map[string]string, error) {
	out := make(map[string]string)
	name := ""
	var body strings.Builder
	flush := func() error {
		if name == "" {
			return nil
		}
		statement := strings.TrimSpace(body.String())
		if statement == "" {
			return fmt.Errorf("statement %s is empty", name)
		}
		if _, dup := out[name]; dup {
			return fmt.Errorf("statement %s is defined twice", name)
		}
		out[name] = statement
		return nil
	}
	for line := range strings.SplitSeq(text, "\n") {
		if rest, ok := strings.CutPrefix(strings.TrimSpace(line), nameMarker); ok {
			if err := flush(); err != nil {
				return nil, err
			}
			name = strings.TrimSpace(rest)
			body.Reset()
			continue
		}
		if name != "" {
			body.WriteString(line)
			body.WriteByte('\n')
		}
	}
	if err := flush(); err != nil {
		return nil, err
	}
	return out, nil
}

// Close closes the statements and the database.
func (s *Store) Close() error {
	var first error
	for _, stmt := range s.stmts {
		if err := stmt.Close(); err != nil && first == nil {
			first = err
		}
	}
	if err := s.db.Close(); err != nil && first == nil {
		first = err
	}
	return first
}

// inTx runs fn in one transaction and commits it when fn succeeds.
func (s *Store) inTx(ctx context.Context, fn func(tx *sql.Tx) error) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	if err := fn(tx); err != nil {
		_ = tx.Rollback()
		return err
	}
	return tx.Commit()
}

// exec runs the named statement inside the transaction.
func (s *Store) exec(ctx context.Context, tx *sql.Tx, name string, args ...any) (sql.Result, error) {
	stmt := tx.StmtContext(ctx, s.stmts[name])
	defer func() { _ = stmt.Close() }()
	return stmt.ExecContext(ctx, args...)
}

func ms(t time.Time) int64 {
	if t.IsZero() {
		return 0
	}
	return t.UnixMilli()
}

func at(ms int64) time.Time {
	if ms == 0 {
		return time.Time{}
	}
	return time.UnixMilli(ms)
}

// UpsertAgent creates the agent or updates its seat and last-seen time (core.Store).
func (s *Store) UpsertAgent(ctx context.Context, name, seat string, now time.Time) error {
	return s.inTx(ctx, func(tx *sql.Tx) error {
		var current sql.NullString
		seatOf := tx.StmtContext(ctx, s.stmts["AgentSeat"])
		defer func() { _ = seatOf.Close() }()
		err := seatOf.QueryRowContext(ctx, name).Scan(&current)
		exists := err == nil
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		if exists && seat != "" && current.Valid && current.String != seat {
			return core.ErrNameTaken
		}
		if seat != "" {
			// A seat has one name: taking a name in a seat releases the seat's previous name.
			if _, err := s.exec(ctx, tx, "ReleaseSeat", seat, name); err != nil {
				return err
			}
		}
		if !exists {
			_, err = s.exec(ctx, tx, "InsertAgent", name, seat, ms(now))
			return err
		}
		_, err = s.exec(ctx, tx, "UpdateAgent", ms(now), seat, name)
		return err
	})
}

type scanner interface {
	Scan(dest ...any) error
}

func scanAgent(row scanner) (core.Agent, error) {
	var (
		a                               core.Agent
		seat                            sql.NullString
		seen, public, private, cursorID int64
	)
	if err := row.Scan(&a.Name, &seat, &seen, &public, &private, &cursorID); err != nil {
		return core.Agent{}, err
	}
	a.Seat = seat.String
	a.LastSeen = at(seen)
	a.LastPublicAt = at(public)
	a.LastPrivateAt = at(private)
	a.RoomCursor = cursorID
	return a, nil
}

func (s *Store) oneAgent(ctx context.Context, statement, key string) (core.Agent, bool, error) {
	a, err := scanAgent(s.stmts[statement].QueryRowContext(ctx, key))
	if errors.Is(err, sql.ErrNoRows) {
		return core.Agent{}, false, nil
	}
	if err != nil {
		return core.Agent{}, false, err
	}
	return a, true, nil
}

// Agent returns the agent with that name (core.Store).
func (s *Store) Agent(ctx context.Context, name string) (core.Agent, bool, error) {
	return s.oneAgent(ctx, "AgentByName", name)
}

// AgentBySeat returns the agent bound to the seat (core.Store).
func (s *Store) AgentBySeat(ctx context.Context, seat string) (core.Agent, bool, error) {
	if seat == "" {
		return core.Agent{}, false, nil
	}
	return s.oneAgent(ctx, "AgentBySeat", seat)
}

// Agents lists agents seen at or after since, most recently seen first (core.Store).
func (s *Store) Agents(ctx context.Context, since time.Time, limit int) ([]core.Agent, error) {
	rows, err := s.stmts["Agents"].QueryContext(ctx, ms(since), limit)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []core.Agent
	for rows.Next() {
		a, err := scanAgent(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// Touch sets the agent's last-seen time (core.Store).
func (s *Store) Touch(ctx context.Context, name string, now time.Time) error {
	_, err := s.stmts["Touch"].ExecContext(ctx, ms(now), name)
	return err
}

// InsertRoom stores a room message and the sender's last public send time (core.Store).
func (s *Store) InsertRoom(ctx context.Context, sender, body string, now time.Time) (core.Message, error) {
	return s.insert(ctx, sender, core.KindRoom, nil, body, now)
}

// InsertPM stores a private message with one delivery row per recipient (core.Store).
func (s *Store) InsertPM(
	ctx context.Context, sender string, recipients []string, body string, now time.Time,
) (core.Message, error) {
	return s.insert(ctx, sender, core.KindPM, recipients, body, now)
}

func (s *Store) insert(
	ctx context.Context, sender string, kind core.Kind, recipients []string, body string, now time.Time,
) (core.Message, error) {
	m := core.Message{At: now, Sender: sender, Kind: kind, Body: body}
	err := s.inTx(ctx, func(tx *sql.Tx) error {
		res, err := s.exec(ctx, tx, "InsertMessage", ms(now), sender, string(kind), body)
		if err != nil {
			return err
		}
		if m.ID, err = res.LastInsertId(); err != nil {
			return err
		}
		sent := "SetLastPublic"
		if kind == core.KindPM {
			sent = "SetLastPrivate"
			for _, recipient := range recipients {
				// INSERT OR IGNORE: a recipient named twice gets one row.
				if _, err := s.exec(ctx, tx, "InsertDelivery", m.ID, recipient); err != nil {
					return err
				}
			}
		}
		_, err = s.exec(ctx, tx, sent, ms(now), sender)
		return err
	})
	if err != nil {
		return core.Message{}, err
	}
	return m, nil
}

// scanMessages reads every row. The caller closes rows.
func scanMessages(rows *sql.Rows) ([]core.Message, error) {
	var out []core.Message
	for rows.Next() {
		var (
			m      core.Message
			sentAt int64
			kind   string
		)
		if err := rows.Scan(&m.ID, &sentAt, &m.Sender, &kind, &m.Body); err != nil {
			return nil, err
		}
		m.At = at(sentAt)
		m.Kind = core.Kind(kind)
		out = append(out, m)
	}
	return out, rows.Err()
}

// unread reads the recipient's undelivered private messages inside the transaction.
func (s *Store) unread(ctx context.Context, tx *sql.Tx, recipient string) ([]core.Message, error) {
	stmt := tx.StmtContext(ctx, s.stmts["UnreadPMs"])
	defer func() { _ = stmt.Close() }()
	rows, err := stmt.QueryContext(ctx, recipient)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	return scanMessages(rows)
}

// UnreadPMs returns the recipient's undelivered private messages, oldest first, and marks them delivered, in one
// transaction, so two readers never both get a message (core.Store).
func (s *Store) UnreadPMs(ctx context.Context, recipient string, now time.Time) ([]core.Message, error) {
	var out []core.Message
	err := s.inTx(ctx, func(tx *sql.Tx) error {
		var err error
		// The rows are read and closed inside unread, before the update: the one connection cannot run a
		// statement while rows are open.
		if out, err = s.unread(ctx, tx, recipient); err != nil || len(out) == 0 {
			return err
		}
		_, err = s.exec(ctx, tx, "MarkDelivered", ms(now), recipient)
		return err
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// HasUnreadPM reports whether the recipient has an undelivered private message (core.Store).
func (s *Store) HasUnreadPM(ctx context.Context, recipient string) (bool, error) {
	var has bool
	err := s.stmts["HasUnreadPM"].QueryRowContext(ctx, recipient).Scan(&has)
	return has, err
}

// RoomTail returns the newest limit room messages, oldest first (core.Store).
func (s *Store) RoomTail(ctx context.Context, limit int) ([]core.Message, error) {
	rows, err := s.stmts["RoomTail"].QueryContext(ctx, limit)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	return scanMessages(rows)
}

// RoomSince returns the newest limit room messages after an id that exclude did not send, oldest first
// (core.Store).
func (s *Store) RoomSince(ctx context.Context, after int64, exclude string, limit int) ([]core.Message, error) {
	rows, err := s.stmts["RoomSince"].QueryContext(ctx, after, exclude, limit)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	return scanMessages(rows)
}

// SetRoomCursor records the newest room message id delivered to the agent by a hook (core.Store).
func (s *Store) SetRoomCursor(ctx context.Context, name string, id int64) error {
	_, err := s.stmts["SetRoomCursor"].ExecContext(ctx, id, name)
	return err
}

// MaxRoomID returns the id of the newest room message, or 0 (core.Store).
func (s *Store) MaxRoomID(ctx context.Context) (int64, error) {
	var id int64
	err := s.stmts["MaxRoomID"].QueryRowContext(ctx).Scan(&id)
	return id, err
}

// Prune deletes messages older than keepHours and all but the newest keepMessages; delivery rows follow by the
// foreign key. It returns how many messages it deleted (core.Store).
func (s *Store) Prune(ctx context.Context, now time.Time, keepHours, keepMessages int) (int, error) {
	var deleted int64
	err := s.inTx(ctx, func(tx *sql.Tx) error {
		cutoff := now.Add(-time.Duration(keepHours) * time.Hour)
		byAge, err := s.exec(ctx, tx, "PruneByAge", ms(cutoff))
		if err != nil {
			return err
		}
		aged, err := byAge.RowsAffected()
		if err != nil {
			return err
		}
		// The keepMessages-th newest message sits at offset keepMessages-1; everything below it goes.
		byCount, err := s.exec(ctx, tx, "PruneByCount", max(keepMessages, 1)-1)
		if err != nil {
			return err
		}
		counted, err := byCount.RowsAffected()
		if err != nil {
			return err
		}
		deleted = aged + counted
		return nil
	})
	return int(deleted), err
}

// BindHookSession records that the hook session belongs to the name (core.Bindings).
func (s *Store) BindHookSession(ctx context.Context, session, name string, now time.Time) error {
	_, err := s.stmts["BindHookSession"].ExecContext(ctx, session, name, ms(now))
	return err
}

// HookSessions returns every stored binding, session to name (core.Bindings).
func (s *Store) HookSessions(ctx context.Context) (map[string]string, error) {
	rows, err := s.stmts["HookSessions"].QueryContext(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := make(map[string]string)
	for rows.Next() {
		var session, name string
		if err := rows.Scan(&session, &name); err != nil {
			return nil, err
		}
		out[session] = name
	}
	return out, rows.Err()
}

// PruneHookSessions deletes bindings last used before the given time (core.Bindings).
func (s *Store) PruneHookSessions(ctx context.Context, before time.Time) (int, error) {
	res, err := s.stmts["PruneHookSessions"].ExecContext(ctx, ms(before))
	if err != nil {
		return 0, err
	}
	n, err := res.RowsAffected()
	return int(n), err
}

// MoveSeat rebinds the name from one seat to another; it changes nothing unless the name is bound to from. A
// seat has one name, so whatever other name held the new seat is released first (core.Bindings).
func (s *Store) MoveSeat(ctx context.Context, name, from, to string) error {
	if from == "" || to == "" || from == to {
		return nil
	}
	return s.inTx(ctx, func(tx *sql.Tx) error {
		var current sql.NullString
		seatOf := tx.StmtContext(ctx, s.stmts["AgentSeat"])
		defer func() { _ = seatOf.Close() }()
		err := seatOf.QueryRowContext(ctx, name).Scan(&current)
		if errors.Is(err, sql.ErrNoRows) || (err == nil && current.String != from) {
			return nil
		}
		if err != nil {
			return err
		}
		if _, err := s.exec(ctx, tx, "ReleaseSeat", to, name); err != nil {
			return err
		}
		_, err = s.exec(ctx, tx, "MoveSeat", to, name, from)
		return err
	})
}
