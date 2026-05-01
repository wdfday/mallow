-- Strategist database schema.
-- Run manually: psql -U mallow -d strategist -f strategist_schema.sql

CREATE TABLE IF NOT EXISTS users (
    chat_id    BIGINT      PRIMARY KEY,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE TABLE IF NOT EXISTS conversations (
    session_id  TEXT        PRIMARY KEY,
    chat_id     BIGINT      NOT NULL REFERENCES users(chat_id) ON DELETE CASCADE,
    source      TEXT        NOT NULL DEFAULT 'telegram',
    is_current  BOOL        NOT NULL DEFAULT true,
    started_at  TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    last_active TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE UNIQUE INDEX IF NOT EXISTS idx_conversations_current ON conversations(chat_id, source) WHERE is_current = true;
CREATE INDEX        IF NOT EXISTS idx_conversations_user    ON conversations(chat_id, last_active DESC);

CREATE TABLE IF NOT EXISTS messages (
    id         BIGSERIAL   PRIMARY KEY,
    session_id TEXT        NOT NULL REFERENCES conversations(session_id) ON DELETE CASCADE,
    role       TEXT        NOT NULL CHECK (role IN ('user', 'assistant')),
    content    TEXT        NOT NULL,
    payload    JSONB,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_messages_session ON messages(session_id, created_at);
