-- The whole schema of mairc. Applied at every start; every statement is idempotent.
-- Times are Unix milliseconds. 0 means "never" for the agent's send times.

CREATE TABLE IF NOT EXISTS agent (
  name            TEXT PRIMARY KEY,
  seat            TEXT UNIQUE,              -- NULL when not bound to a seat
  last_seen_ms    INTEGER NOT NULL,
  last_public_ms  INTEGER NOT NULL DEFAULT 0,
  last_private_ms INTEGER NOT NULL DEFAULT 0,
  room_cursor     INTEGER NOT NULL DEFAULT 0
) WITHOUT ROWID;

CREATE TABLE IF NOT EXISTS message (
  id     INTEGER PRIMARY KEY AUTOINCREMENT,
  at_ms  INTEGER NOT NULL,
  sender TEXT NOT NULL,
  kind   TEXT NOT NULL CHECK (kind IN ('room', 'pm')),
  body   TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS message_room ON message (id) WHERE kind = 'room';
CREATE INDEX IF NOT EXISTS message_at ON message (at_ms);

CREATE TABLE IF NOT EXISTS delivery (
  message_id   INTEGER NOT NULL REFERENCES message (id) ON DELETE CASCADE,
  recipient    TEXT NOT NULL,
  delivered_ms INTEGER,                     -- NULL until read or delivered by a hook
  PRIMARY KEY (message_id, recipient)
) WITHOUT ROWID;
CREATE INDEX IF NOT EXISTS delivery_unread ON delivery (recipient) WHERE delivered_ms IS NULL;

-- Which name an agent CLI session's hook resolved to, so a daemon restart does not forget it.
CREATE TABLE IF NOT EXISTS hook_session (
  session      TEXT PRIMARY KEY,
  name         TEXT NOT NULL,
  last_used_ms INTEGER NOT NULL
) WITHOUT ROWID;
