-- Every statement the store runs, by name. The loader splits this file at the "-- name:" markers and prepares
-- each statement once. Parameters are positional, in the order the Go code binds them.

-- name: AgentSeat
SELECT seat FROM agent WHERE name = ?;

-- name: ReleaseSeat
UPDATE agent SET seat = NULL WHERE seat = ? AND name <> ?;

-- name: InsertAgent
INSERT INTO agent (name, seat, last_seen_ms) VALUES (?, NULLIF(?, ''), ?);

-- name: UpdateAgent
UPDATE agent SET last_seen_ms = ?, seat = COALESCE(NULLIF(?, ''), seat) WHERE name = ?;

-- name: AgentByName
SELECT name, seat, last_seen_ms, last_public_ms, last_private_ms, room_cursor FROM agent WHERE name = ?;

-- name: AgentBySeat
SELECT name, seat, last_seen_ms, last_public_ms, last_private_ms, room_cursor FROM agent WHERE seat = ?;

-- name: Agents
SELECT name, seat, last_seen_ms, last_public_ms, last_private_ms, room_cursor FROM agent
WHERE last_seen_ms >= ? ORDER BY last_seen_ms DESC, name LIMIT ?;

-- name: Touch
UPDATE agent SET last_seen_ms = ? WHERE name = ?;

-- name: InsertMessage
INSERT INTO message (at_ms, sender, kind, body) VALUES (?, ?, ?, ?);

-- name: SetLastPublic
UPDATE agent SET last_public_ms = ? WHERE name = ?;

-- name: SetLastPrivate
UPDATE agent SET last_private_ms = ? WHERE name = ?;

-- name: InsertDelivery
INSERT OR IGNORE INTO delivery (message_id, recipient) VALUES (?, ?);

-- name: UnreadPMs
SELECT m.id, m.at_ms, m.sender, m.kind, m.body FROM delivery d JOIN message m ON m.id = d.message_id
WHERE d.recipient = ? AND d.delivered_ms IS NULL ORDER BY m.id;

-- name: MarkDelivered
UPDATE delivery SET delivered_ms = ? WHERE recipient = ? AND delivered_ms IS NULL;

-- name: HasUnreadPM
SELECT EXISTS (SELECT 1 FROM delivery WHERE recipient = ? AND delivered_ms IS NULL);

-- name: RoomTail
SELECT id, at_ms, sender, kind, body FROM (
  SELECT id, at_ms, sender, kind, body FROM message WHERE kind = 'room' ORDER BY id DESC LIMIT ?
) ORDER BY id;

-- name: RoomSince
SELECT id, at_ms, sender, kind, body FROM (
  SELECT id, at_ms, sender, kind, body FROM message
  WHERE kind = 'room' AND id > ? AND sender <> ? ORDER BY id DESC LIMIT ?
) ORDER BY id;

-- name: SetRoomCursor
UPDATE agent SET room_cursor = ? WHERE name = ?;

-- name: MaxRoomID
SELECT COALESCE(MAX(id), 0) FROM message WHERE kind = 'room';

-- name: PruneByAge
DELETE FROM message WHERE at_ms < ?;

-- name: PruneByCount
DELETE FROM message WHERE id < (SELECT id FROM message ORDER BY id DESC LIMIT 1 OFFSET ?);

-- name: BindHookSession
INSERT INTO hook_session (session, name, last_used_ms) VALUES (?, ?, ?)
ON CONFLICT (session) DO UPDATE SET name = excluded.name, last_used_ms = excluded.last_used_ms;

-- name: HookSessions
SELECT session, name FROM hook_session;

-- name: PruneHookSessions
DELETE FROM hook_session WHERE last_used_ms < ?;

-- name: MoveSeat
UPDATE agent SET seat = ? WHERE name = ? AND seat = ?;

-- name: PrivateSince
SELECT d.recipient, MAX(m.at_ms) FROM message m JOIN delivery d ON d.message_id = m.id
WHERE m.kind = 'pm' AND m.sender = ? AND m.at_ms >= ? GROUP BY d.recipient;
