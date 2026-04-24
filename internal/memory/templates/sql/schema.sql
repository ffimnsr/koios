CREATE TABLE IF NOT EXISTS chunks (
id         TEXT PRIMARY KEY,
peer_id    TEXT NOT NULL,
content    TEXT NOT NULL,
created_at INTEGER NOT NULL,
tags       TEXT NOT NULL DEFAULT '',
category   TEXT NOT NULL DEFAULT '',
retention_class TEXT NOT NULL DEFAULT 'working',
exposure_policy TEXT NOT NULL DEFAULT 'auto',
expires_at INTEGER NOT NULL DEFAULT 0,
capture_kind TEXT NOT NULL DEFAULT 'manual',
capture_reason TEXT NOT NULL DEFAULT '',
confidence REAL NOT NULL DEFAULT 1.0,
source_session_key TEXT NOT NULL DEFAULT '',
source_message_id TEXT NOT NULL DEFAULT '',
source_run_id TEXT NOT NULL DEFAULT '',
source_hook TEXT NOT NULL DEFAULT '',
source_candidate_id TEXT NOT NULL DEFAULT '',
source_excerpt TEXT NOT NULL DEFAULT ''
);
CREATE INDEX IF NOT EXISTS idx_chunks_peer_created_at ON chunks(peer_id, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_chunks_expires_at ON chunks(expires_at);
CREATE VIRTUAL TABLE IF NOT EXISTS chunks_fts USING fts5(
id      UNINDEXED,
peer_id UNINDEXED,
content,
tokenize = 'unicode61 remove_diacritics 2'
);
CREATE TABLE IF NOT EXISTS chunk_embeddings (
chunk_id  TEXT PRIMARY KEY,
embedding BLOB NOT NULL
);

CREATE TABLE IF NOT EXISTS memory_candidates (
id TEXT PRIMARY KEY,
peer_id TEXT NOT NULL,
content TEXT NOT NULL,
created_at INTEGER NOT NULL,
tags TEXT NOT NULL DEFAULT '',
category TEXT NOT NULL DEFAULT '',
retention_class TEXT NOT NULL DEFAULT 'working',
exposure_policy TEXT NOT NULL DEFAULT 'auto',
expires_at INTEGER NOT NULL DEFAULT 0,
status TEXT NOT NULL DEFAULT 'pending',
review_reason TEXT NOT NULL DEFAULT '',
reviewed_at INTEGER NOT NULL DEFAULT 0,
result_chunk_id TEXT NOT NULL DEFAULT '',
merge_into_id TEXT NOT NULL DEFAULT '',
capture_kind TEXT NOT NULL DEFAULT 'manual',
source_session_key TEXT NOT NULL DEFAULT '',
source_excerpt TEXT NOT NULL DEFAULT ''
);
CREATE INDEX IF NOT EXISTS idx_memory_candidates_peer_status_created_at ON memory_candidates(peer_id, status, created_at DESC);

CREATE TABLE IF NOT EXISTS memory_preferences (
id TEXT PRIMARY KEY,
peer_id TEXT NOT NULL,
kind TEXT NOT NULL DEFAULT 'preference',
name TEXT NOT NULL,
value TEXT NOT NULL,
category TEXT NOT NULL DEFAULT '',
scope TEXT NOT NULL DEFAULT 'global',
scope_ref TEXT NOT NULL DEFAULT '',
confidence REAL NOT NULL DEFAULT 1.0,
last_confirmed_at INTEGER NOT NULL DEFAULT 0,
created_at INTEGER NOT NULL,
updated_at INTEGER NOT NULL,
source_session_key TEXT NOT NULL DEFAULT '',
source_excerpt TEXT NOT NULL DEFAULT ''
);
CREATE INDEX IF NOT EXISTS idx_memory_preferences_peer_kind_scope ON memory_preferences(peer_id, kind, scope, updated_at DESC);
CREATE INDEX IF NOT EXISTS idx_memory_preferences_peer_confirmed ON memory_preferences(peer_id, last_confirmed_at DESC, confidence DESC);

CREATE TABLE IF NOT EXISTS memory_entities (
id TEXT PRIMARY KEY,
peer_id TEXT NOT NULL,
kind TEXT NOT NULL DEFAULT 'topic',
name TEXT NOT NULL,
aliases TEXT NOT NULL DEFAULT '',
notes TEXT NOT NULL DEFAULT '',
created_at INTEGER NOT NULL,
updated_at INTEGER NOT NULL,
last_seen_at INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX IF NOT EXISTS idx_memory_entities_peer_kind_name ON memory_entities(peer_id, kind, name);

CREATE VIRTUAL TABLE IF NOT EXISTS memory_entities_fts USING fts5(
id UNINDEXED,
peer_id UNINDEXED,
name,
aliases,
notes,
tokenize = 'unicode61 remove_diacritics 2'
);

CREATE TABLE IF NOT EXISTS memory_entity_chunks (
entity_id TEXT NOT NULL,
chunk_id TEXT NOT NULL,
created_at INTEGER NOT NULL,
PRIMARY KEY(entity_id, chunk_id),
FOREIGN KEY(entity_id) REFERENCES memory_entities(id) ON DELETE CASCADE,
FOREIGN KEY(chunk_id) REFERENCES chunks(id) ON DELETE CASCADE
);

CREATE TABLE IF NOT EXISTS memory_entity_edges (
id TEXT PRIMARY KEY,
peer_id TEXT NOT NULL,
source_entity_id TEXT NOT NULL,
target_entity_id TEXT NOT NULL,
relation TEXT NOT NULL,
notes TEXT NOT NULL DEFAULT '',
created_at INTEGER NOT NULL,
updated_at INTEGER NOT NULL,
FOREIGN KEY(source_entity_id) REFERENCES memory_entities(id) ON DELETE CASCADE,
FOREIGN KEY(target_entity_id) REFERENCES memory_entities(id) ON DELETE CASCADE
);
CREATE UNIQUE INDEX IF NOT EXISTS idx_memory_entity_edges_unique ON memory_entity_edges(peer_id, source_entity_id, target_entity_id, relation);
CREATE INDEX IF NOT EXISTS idx_memory_entity_edges_source ON memory_entity_edges(peer_id, source_entity_id, updated_at DESC);
CREATE INDEX IF NOT EXISTS idx_memory_entity_edges_target ON memory_entity_edges(peer_id, target_entity_id, updated_at DESC);
