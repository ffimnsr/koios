// Package memory provides a SQLite-backed long-term memory store with BM25
// full-text search (via FTS5) and optional vector-embedding hybrid reranking.
//
// Architecture summary:
//
// chunks        – raw content records keyed by a random hex ID
// chunks_fts    – FTS5 virtual table for BM25 keyword search (standalone)
// chunk_embeddings – packed float32 embeddings for cosine similarity reranking
//
// When an Embedder is configured the store indexes embedding vectors at insert
// time (asynchronously) and uses hybrid BM25+cosine scoring during search.
// When no Embedder is available the store falls back to BM25-only search so
// that the memory layer is always functional.
package memory

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/binary"
	"fmt"
	"log/slog"
	"math"
	"sort"
	"strings"
	"time"

	_ "modernc.org/sqlite" // register "sqlite" driver
)

// SearchResult is a single memory chunk returned by a search query.
type SearchResult struct {
	ID      string
	PeerID  string
	Content string
	Score   float64 // BM25 rank (negative) or cosine similarity [0,1]
}

// Store is a SQLite-backed memory store.
type Store struct {
	db       *sql.DB
	embedder Embedder // nil = BM25-only mode
}

// New opens (or creates) the SQLite database at dbPath and initialises the
// schema. Pass a nil embedder to operate in BM25-only mode.
func New(dbPath string, embedder Embedder) (*Store, error) {
	db, err := sql.Open("sqlite", dbPath+"?_journal_mode=WAL&_foreign_keys=on")
	if err != nil {
		return nil, fmt.Errorf("memory: open db: %w", err)
	}
	db.SetMaxOpenConns(1) // SQLite WAL supports one writer
	if err := migrate(db); err != nil {
		db.Close()
		return nil, fmt.Errorf("memory: migrate: %w", err)
	}
	return &Store{db: db, embedder: embedder}, nil
}

// Close releases the database connection.
func (s *Store) Close() error {
	return s.db.Close()
}

// Insert adds a content chunk for a peer to the memory store.
// Embedding is indexed asynchronously in a best-effort goroutine.
func (s *Store) Insert(ctx context.Context, peerID, content string) error {
	id := newID()
	now := time.Now().Unix()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("memory insert: begin tx: %w", err)
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO chunks(id, peer_id, content, created_at) VALUES (?,?,?,?)`,
		id, peerID, content, now); err != nil {
		return fmt.Errorf("memory insert: chunks: %w", err)
	}
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO chunks_fts(id, peer_id, content) VALUES (?,?,?)`,
		id, peerID, content); err != nil {
		return fmt.Errorf("memory insert: fts: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("memory insert: commit: %w", err)
	}
	if s.embedder != nil {
		go s.indexEmbedding(id, content)
	}
	return nil
}

// Search returns the top-K chunks matching query for the given peer.
// When an Embedder is set, BM25 candidates are reranked by cosine similarity.
func (s *Store) Search(ctx context.Context, peerID, query string, topK int) ([]SearchResult, error) {
	if query = strings.TrimSpace(sanitizeFTSQuery(query)); query == "" {
		return nil, nil
	}
	candidates, err := s.bm25Search(ctx, peerID, query, topK*4)
	if err != nil {
		return nil, err
	}
	if len(candidates) == 0 {
		return nil, nil
	}
	if s.embedder == nil {
		if len(candidates) > topK {
			candidates = candidates[:topK]
		}
		return candidates, nil
	}
	// Hybrid reranking: compute cosine similarity for BM25 candidates.
	queryEmb, err := s.embedder.Embed(ctx, query)
	if err != nil {
		slog.Warn("memory search: embedding failed, using BM25 only", "err", err)
		if len(candidates) > topK {
			candidates = candidates[:topK]
		}
		return candidates, nil
	}
	for i := range candidates {
		emb, _ := s.loadEmbedding(candidates[i].ID)
		if emb != nil {
			candidates[i].Score = cosine(queryEmb, emb)
		}
	}
	sort.Slice(candidates, func(i, j int) bool {
		return candidates[i].Score > candidates[j].Score
	})
	if len(candidates) > topK {
		candidates = candidates[:topK]
	}
	return candidates, nil
}

// Chunk is a single memory record returned by List.
type Chunk struct {
	ID        string `json:"id"`
	PeerID    string `json:"peer_id"`
	Content   string `json:"content"`
	CreatedAt int64  `json:"created_at"`
}

// List returns all chunks stored for a peer, ordered by creation time descending.
func (s *Store) List(ctx context.Context, peerID string, limit int) ([]Chunk, error) {
	if limit <= 0 {
		limit = 100
	}
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, peer_id, content, created_at FROM chunks WHERE peer_id = ? ORDER BY created_at DESC LIMIT ?`,
		peerID, limit)
	if err != nil {
		return nil, fmt.Errorf("memory list: %w", err)
	}
	defer rows.Close()
	var chunks []Chunk
	for rows.Next() {
		var c Chunk
		if err := rows.Scan(&c.ID, &c.PeerID, &c.Content, &c.CreatedAt); err != nil {
			continue
		}
		chunks = append(chunks, c)
	}
	return chunks, rows.Err()
}

// DeleteChunk removes a single memory chunk by ID, enforcing peer ownership.
func (s *Store) DeleteChunk(ctx context.Context, peerID, chunkID string) error {
	// Verify ownership before deleting.
	var owner string
	err := s.db.QueryRowContext(ctx, `SELECT peer_id FROM chunks WHERE id = ?`, chunkID).Scan(&owner)
	if err == sql.ErrNoRows {
		return fmt.Errorf("chunk %s not found", chunkID)
	}
	if err != nil {
		return fmt.Errorf("memory delete: lookup: %w", err)
	}
	if owner != peerID {
		return fmt.Errorf("chunk %s does not belong to peer", chunkID)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("memory delete: begin tx: %w", err)
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `DELETE FROM chunk_embeddings WHERE chunk_id = ?`, chunkID); err != nil {
		return fmt.Errorf("memory delete: embeddings: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM chunks_fts WHERE id = ?`, chunkID); err != nil {
		return fmt.Errorf("memory delete: fts: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM chunks WHERE id = ?`, chunkID); err != nil {
		return fmt.Errorf("memory delete: chunks: %w", err)
	}
	return tx.Commit()
}

// DeletePeer removes all memory chunks for the given peer.
func (s *Store) DeletePeer(ctx context.Context, peerID string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	rows, err := tx.QueryContext(ctx, `SELECT id FROM chunks WHERE peer_id = ?`, peerID)
	if err != nil {
		return err
	}
	// Collect IDs first so we can delete from FTS and embeddings tables.
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err == nil {
			ids = append(ids, id)
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	for _, id := range ids {
		if _, err := tx.ExecContext(ctx, `DELETE FROM chunk_embeddings WHERE chunk_id = ?`, id); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM chunks_fts WHERE id = ?`, id); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM chunks WHERE id = ?`, id); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// — internal ——————————————————————————————————————————————————————————————————

func (s *Store) bm25Search(ctx context.Context, peerID, query string, limit int) ([]SearchResult, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, peer_id, content, rank
   FROM chunks_fts
  WHERE chunks_fts MATCH ?
    AND peer_id = ?
  ORDER BY rank
  LIMIT ?`,
		query, peerID, limit)
	if err != nil {
		return nil, fmt.Errorf("memory bm25: %w", err)
	}
	defer rows.Close()
	var results []SearchResult
	for rows.Next() {
		var r SearchResult
		var rank float64
		if err := rows.Scan(&r.ID, &r.PeerID, &r.Content, &rank); err != nil {
			continue
		}
		r.Score = -rank // negate: FTS5 rank is negative; higher Score = better match
		results = append(results, r)
	}
	return results, rows.Err()
}

func (s *Store) indexEmbedding(chunkID, content string) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	emb, err := s.embedder.Embed(ctx, content)
	if err != nil {
		slog.Warn("memory: embed failed", "chunk", chunkID, "err", err)
		return
	}
	blob := encodeEmbedding(emb)
	if _, err := s.db.ExecContext(ctx,
		`INSERT OR REPLACE INTO chunk_embeddings(chunk_id, embedding) VALUES (?,?)`,
		chunkID, blob); err != nil {
		slog.Warn("memory: store embedding", "chunk", chunkID, "err", err)
	}
}

func (s *Store) loadEmbedding(chunkID string) ([]float32, error) {
	var blob []byte
	err := s.db.QueryRowContext(context.Background(),
		`SELECT embedding FROM chunk_embeddings WHERE chunk_id = ?`, chunkID).Scan(&blob)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return decodeEmbedding(blob), nil
}

// — schema ————————————————————————————————————————————————————————————————————

func migrate(db *sql.DB) error {
	_, err := db.Exec(`
CREATE TABLE IF NOT EXISTS chunks (
id         TEXT PRIMARY KEY,
peer_id    TEXT NOT NULL,
content    TEXT NOT NULL,
created_at INTEGER NOT NULL
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
`)
	return err
}

// — helpers ———————————————————————————————————————————————————————————————————

func newID() string {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		// crypto/rand failure is extremely unlikely; fall back to timestamp.
		return fmt.Sprintf("%x", time.Now().UnixNano())
	}
	return fmt.Sprintf("%x", b)
}

// sanitizeFTSQuery strips characters that have special meaning in FTS5 queries
// to prevent query injection errors.
func sanitizeFTSQuery(q string) string {
	var b strings.Builder
	for _, c := range q {
		switch c {
		case '"', '(', ')', '*', '^', '-', '+', ':':
			b.WriteByte(' ')
		default:
			b.WriteRune(c)
		}
	}
	return b.String()
}

func encodeEmbedding(v []float32) []byte {
	b := make([]byte, len(v)*4)
	for i, f := range v {
		binary.LittleEndian.PutUint32(b[i*4:], math.Float32bits(f))
	}
	return b
}

func decodeEmbedding(b []byte) []float32 {
	v := make([]float32, len(b)/4)
	for i := range v {
		v[i] = math.Float32frombits(binary.LittleEndian.Uint32(b[i*4:]))
	}
	return v
}

func cosine(a, b []float32) float64 {
	if len(a) != len(b) || len(a) == 0 {
		return 0
	}
	var dot, na, nb float64
	for i := range a {
		fa, fb := float64(a[i]), float64(b[i])
		dot += fa * fb
		na += fa * fa
		nb += fb * fb
	}
	if na == 0 || nb == 0 {
		return 0
	}
	return dot / (math.Sqrt(na) * math.Sqrt(nb))
}
