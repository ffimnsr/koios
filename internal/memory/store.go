// Package memory provides a SQLite-backed long-term memory store with BM25
// full-text search (via FTS5) and optional vector-embedding hybrid reranking.
//
// Architecture summary:
//
// chunks        - raw content records keyed by a random hex ID
// chunks_fts    - FTS5 virtual table for BM25 keyword search (standalone)
// chunk_embeddings - packed float32 embeddings for cosine similarity reranking
//
// When an Embedder is configured the store indexes embedding vectors at insert
// time (asynchronously) and uses hybrid BM25+cosine scoring during search.
// When no Embedder is available the store falls back to BM25-only search so
// that the memory layer is always functional.
package memory

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"time"

	"github.com/ffimnsr/koios/internal/memory/milvus"
	_ "modernc.org/sqlite"
)

// Store is a SQLite-backed memory store with optional Milvus vector search.
type Store struct {
	db       *sql.DB
	embedder Embedder
	milvus   *milvus.Client
}

// New opens (or creates) the SQLite database at dbPath and initialises the schema.
func New(dbPath string, embedder Embedder) (*Store, error) {
	return NewWithMilvus(dbPath, embedder, nil)
}

// NewWithMilvus opens the SQLite database and optionally wires up a Milvus client.
func NewWithMilvus(dbPath string, embedder Embedder, mv *milvus.Client) (*Store, error) {
	db, err := sql.Open("sqlite", dbPath+"?_journal_mode=WAL&_foreign_keys=on")
	if err != nil {
		return nil, fmt.Errorf("memory: open db: %w", err)
	}
	db.SetMaxOpenConns(1)
	if err := migrate(db); err != nil {
		db.Close()
		return nil, fmt.Errorf("memory: migrate: %w", err)
	}
	if mv != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := mv.EnsureCollection(ctx); err != nil {
			slog.Warn("memory: milvus collection init failed, falling back", "err", err)
			mv = nil
		}
	}
	return &Store{db: db, embedder: embedder, milvus: mv}, nil
}

// Close releases the database connection.
func (s *Store) Close() error {
	return s.db.Close()
}
