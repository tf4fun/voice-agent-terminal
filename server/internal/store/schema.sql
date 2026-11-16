CREATE TABLE sessions (
    id TEXT PRIMARY KEY,
    title TEXT NOT NULL,
    state TEXT NOT NULL,
    created_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL,
    error TEXT NOT NULL DEFAULT ''
);
CREATE TABLE messages (
    id TEXT PRIMARY KEY,
    session_id TEXT NOT NULL REFERENCES sessions(id),
    model TEXT NOT NULL,
    upstream_scope TEXT NOT NULL,
    attempts INTEGER NOT NULL DEFAULT 0,
    input TEXT NOT NULL,
    reply TEXT NOT NULL DEFAULT '',
    state TEXT NOT NULL,
    finish_reason TEXT NOT NULL DEFAULT '',
    error TEXT NOT NULL DEFAULT '',
    revision INTEGER NOT NULL DEFAULT 1,
    created_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL
);
CREATE UNIQUE INDEX active_session_message ON messages(session_id)
WHERE state IN ('queued', 'sending', 'waiting', 'receiving', 'attention', 'unconfirmed');
CREATE INDEX session_messages ON messages(session_id, created_at, id);
