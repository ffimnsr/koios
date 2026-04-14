CREATE TABLE IF NOT EXISTS chunks (
id         TEXT PRIMARY KEY,
peer_id    TEXT NOT NULL,
content    TEXT NOT NULL,
created_at INTEGER NOT NULL,
tags       TEXT NOT NULL DEFAULT '',
category   TEXT NOT NULL DEFAULT ''
);
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
