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

	"github.com/ffimnsr/koios/internal/memory/milvus"
	_ "modernc.org/sqlite" // register "sqlite" driver
)

// SearchResult is a single memory chunk returned by a search query.
type SearchResult struct {
	ID      string
	PeerID  string
	Content string
	Score   float64 // BM25 rank (negative) or cosine similarity [0,1]
}

// Store is a SQLite-backed memory store with optional Milvus vector search.
type Store struct {
	db       *sql.DB
	embedder Embedder       // nil = BM25-only mode
	milvus   *milvus.Client // nil = no Milvus
}

// New opens (or creates) the SQLite database at dbPath and initialises the
// schema. Pass a nil embedder to operate in BM25-only mode.
func New(dbPath string, embedder Embedder) (*Store, error) {
	return NewWithMilvus(dbPath, embedder, nil)
}

// NewWithMilvus opens the SQLite database and optionally wires up a Milvus
// client for hybrid vector search.
func NewWithMilvus(dbPath string, embedder Embedder, mv *milvus.Client) (*Store, error) {
	db, err := sql.Open("sqlite", dbPath+"?_journal_mode=WAL&_foreign_keys=on")
	if err != nil {
		return nil, fmt.Errorf("memory: open db: %w", err)
	}
	db.SetMaxOpenConns(1) // SQLite WAL supports one writer
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

// Insert adds a content chunk for a peer to the memory store.
// Embedding is indexed asynchronously in a best-effort goroutine.
func (s *Store) Insert(ctx context.Context, peerID, content string) error {
	_, err := s.InsertChunk(ctx, peerID, content)
	return err
}

// InsertChunk adds a content chunk for a peer and returns the stored record.
func (s *Store) InsertChunk(ctx context.Context, peerID, content string) (*Chunk, error) {
	return s.InsertChunkWithTags(ctx, peerID, content, nil, "")
}

// InsertChunkWithTags adds a content chunk with optional tags and category.
func (s *Store) InsertChunkWithTags(ctx context.Context, peerID, content string, tags []string, category string) (*Chunk, error) {
	id := newID()
	now := time.Now().Unix()
	tagStr := strings.Join(tags, ",")
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("memory insert: begin tx: %w", err)
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO chunks(id, peer_id, content, created_at, tags, category) VALUES (?,?,?,?,?,?)`,
		id, peerID, content, now, tagStr, category); err != nil {
		return nil, fmt.Errorf("memory insert: chunks: %w", err)
	}
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO chunks_fts(id, peer_id, content) VALUES (?,?,?)`,
		id, peerID, content); err != nil {
		return nil, fmt.Errorf("memory insert: fts: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("memory insert: commit: %w", err)
	}
	// Index embedding asynchronously.
	if s.embedder != nil {
		go s.indexEmbedding(id, content)
	}
	// Index into Milvus asynchronously (requires embedder for vector creation).
	if s.milvus != nil && s.embedder != nil {
		go s.indexMilvus(id, peerID, content, tags, category)
	}
	return &Chunk{
		ID:        id,
		PeerID:    peerID,
		Content:   content,
		CreatedAt: now,
		Tags:      tags,
		Category:  category,
	}, nil
}

// Search returns the top-K chunks matching query for the given peer.
// When Milvus is available, results from both FTS5 and Milvus are merged.
// Otherwise, BM25 candidates are optionally reranked by cosine similarity.
func (s *Store) Search(ctx context.Context, peerID, query string, topK int) ([]SearchResult, error) {
	if query = strings.TrimSpace(sanitizeFTSQuery(query)); query == "" {
		return nil, nil
	}
	candidates, err := s.bm25Search(ctx, peerID, query, topK*4)
	if err != nil {
		return nil, err
	}

	// Try Milvus semantic search when available (requires embedder).
	if s.milvus != nil && s.embedder != nil {
		milvusResults, milvusErr := s.milvusSearch(ctx, peerID, query, topK*2)
		if milvusErr != nil {
			slog.Warn("memory search: milvus failed, using BM25 only", "err", milvusErr)
		} else {
			candidates = mergeResults(candidates, milvusResults, topK)
			return candidates, nil
		}
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
	ID        string   `json:"id"`
	PeerID    string   `json:"peer_id"`
	Content   string   `json:"content"`
	CreatedAt int64    `json:"created_at"`
	Tags      []string `json:"tags,omitempty"`
	Category  string   `json:"category,omitempty"`
}

// List returns all chunks stored for a peer, ordered by creation time descending.
func (s *Store) List(ctx context.Context, peerID string, limit int) ([]Chunk, error) {
	if limit <= 0 {
		limit = 100
	}
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, peer_id, content, created_at, COALESCE(tags,''), COALESCE(category,'') FROM chunks WHERE peer_id = ? ORDER BY created_at DESC LIMIT ?`,
		peerID, limit)
	if err != nil {
		return nil, fmt.Errorf("memory list: %w", err)
	}
	defer rows.Close()
	var chunks []Chunk
	for rows.Next() {
		var c Chunk
		var tagStr string
		if err := rows.Scan(&c.ID, &c.PeerID, &c.Content, &c.CreatedAt, &tagStr, &c.Category); err != nil {
			continue
		}
		if tagStr != "" {
			c.Tags = strings.Split(tagStr, ",")
		}
		chunks = append(chunks, c)
	}
	return chunks, rows.Err()
}

// Recent returns the N most-recently created chunks for a peer in
// chronological order (oldest first). Used by the LCM sliding-window injector.
func (s *Store) Recent(ctx context.Context, peerID string, n int) ([]Chunk, error) {
	if n <= 0 {
		return nil, nil
	}
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, peer_id, content, created_at, COALESCE(tags,''), COALESCE(category,'')
		   FROM chunks WHERE peer_id = ?
		  ORDER BY created_at DESC LIMIT ?`,
		peerID, n)
	if err != nil {
		return nil, fmt.Errorf("memory recent: %w", err)
	}
	defer rows.Close()
	var chunks []Chunk
	for rows.Next() {
		var c Chunk
		var tagStr string
		if err := rows.Scan(&c.ID, &c.PeerID, &c.Content, &c.CreatedAt, &tagStr, &c.Category); err != nil {
			continue
		}
		if tagStr != "" {
			c.Tags = strings.Split(tagStr, ",")
		}
		chunks = append(chunks, c)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	// Reverse to chronological order (oldest first) for cleaner context injection.
	for i, j := 0, len(chunks)-1; i < j; i, j = i+1, j-1 {
		chunks[i], chunks[j] = chunks[j], chunks[i]
	}
	return chunks, nil
}

// GetChunk returns one memory chunk by ID, enforcing peer ownership.
func (s *Store) GetChunk(ctx context.Context, peerID, chunkID string) (*Chunk, error) {
	var c Chunk
	var tagStr string
	err := s.db.QueryRowContext(ctx,
		`SELECT id, peer_id, content, created_at, COALESCE(tags,''), COALESCE(category,'') FROM chunks WHERE id = ?`,
		chunkID,
	).Scan(&c.ID, &c.PeerID, &c.Content, &c.CreatedAt, &tagStr, &c.Category)
	if err == sql.ErrNoRows {
		return nil, fmt.Errorf("chunk %s not found", chunkID)
	}
	if err != nil {
		return nil, fmt.Errorf("memory get: %w", err)
	}
	if c.PeerID != peerID {
		return nil, fmt.Errorf("chunk %s does not belong to peer", chunkID)
	}
	if tagStr != "" {
		c.Tags = strings.Split(tagStr, ",")
	}
	return &c, nil
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
	if err := tx.Commit(); err != nil {
		return err
	}
	if s.milvus != nil {
		go func() {
			cctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			if err := s.milvus.Delete(cctx, []string{chunkID}); err != nil {
				slog.Warn("memory: milvus delete failed", "chunk", chunkID, "err", err)
			}
		}()
	}
	return nil
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

// SearchCompact returns compact search results (preview only, no full content).
func (s *Store) SearchCompact(ctx context.Context, peerID, query string, topK int) ([]SearchResultCompact, error) {
	full, err := s.Search(ctx, peerID, query, topK)
	if err != nil {
		return nil, err
	}
	out := make([]SearchResultCompact, len(full))
	for i, r := range full {
		preview := r.Content
		if len(preview) > 200 {
			preview = preview[:200] + "..."
		}
		out[i] = SearchResultCompact{
			ID:      r.ID,
			PeerID:  r.PeerID,
			Preview: preview,
			Score:   r.Score,
		}
	}
	return out, nil
}

// SearchResultCompact is a lightweight search result for progressive disclosure.
type SearchResultCompact struct {
	ID      string  `json:"id"`
	PeerID  string  `json:"peer_id"`
	Preview string  `json:"preview"`
	Score   float64 `json:"score"`
}

// Timeline returns chunks around a given anchor chunk for the same peer,
// ordered chronologically: depthBefore chunks before and depthAfter chunks after.
func (s *Store) Timeline(ctx context.Context, peerID, anchorID string, depthBefore, depthAfter int) ([]Chunk, error) {
	if depthBefore <= 0 {
		depthBefore = 3
	}
	if depthAfter <= 0 {
		depthAfter = 3
	}
	// Get the anchor's created_at.
	var anchorTime int64
	err := s.db.QueryRowContext(ctx,
		`SELECT created_at FROM chunks WHERE id = ? AND peer_id = ?`,
		anchorID, peerID).Scan(&anchorTime)
	if err != nil {
		return nil, fmt.Errorf("memory timeline: anchor lookup: %w", err)
	}
	var chunks []Chunk
	// Before (older).
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, peer_id, content, created_at, COALESCE(tags,''), COALESCE(category,'')
		   FROM chunks WHERE peer_id = ? AND created_at < ?
		  ORDER BY created_at DESC LIMIT ?`,
		peerID, anchorTime, depthBefore)
	if err != nil {
		return nil, fmt.Errorf("memory timeline before: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var c Chunk
		var tagStr string
		if err := rows.Scan(&c.ID, &c.PeerID, &c.Content, &c.CreatedAt, &tagStr, &c.Category); err != nil {
			continue
		}
		if tagStr != "" {
			c.Tags = strings.Split(tagStr, ",")
		}
		chunks = append(chunks, c)
	}
	rows.Close()
	// Reverse so oldest-first.
	for i, j := 0, len(chunks)-1; i < j; i, j = i+1, j-1 {
		chunks[i], chunks[j] = chunks[j], chunks[i]
	}
	// Anchor itself.
	anchor, err := s.GetChunk(ctx, peerID, anchorID)
	if err == nil {
		chunks = append(chunks, *anchor)
	}
	// After (newer).
	rows2, err := s.db.QueryContext(ctx,
		`SELECT id, peer_id, content, created_at, COALESCE(tags,''), COALESCE(category,'')
		   FROM chunks WHERE peer_id = ? AND created_at > ?
		  ORDER BY created_at ASC LIMIT ?`,
		peerID, anchorTime, depthAfter)
	if err != nil {
		return nil, fmt.Errorf("memory timeline after: %w", err)
	}
	defer rows2.Close()
	for rows2.Next() {
		var c Chunk
		var tagStr string
		if err := rows2.Scan(&c.ID, &c.PeerID, &c.Content, &c.CreatedAt, &tagStr, &c.Category); err != nil {
			continue
		}
		if tagStr != "" {
			c.Tags = strings.Split(tagStr, ",")
		}
		chunks = append(chunks, c)
	}
	return chunks, rows2.Err()
}

// BatchGet retrieves multiple chunks by ID for the given peer.
func (s *Store) BatchGet(ctx context.Context, peerID string, ids []string) ([]Chunk, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	placeholders := make([]string, len(ids))
	args := make([]any, 0, len(ids)+1)
	args = append(args, peerID)
	for i, id := range ids {
		placeholders[i] = "?"
		args = append(args, id)
	}
	query := `SELECT id, peer_id, content, created_at, COALESCE(tags,''), COALESCE(category,'')
	            FROM chunks WHERE peer_id = ? AND id IN (` + strings.Join(placeholders, ",") + `)
	           ORDER BY created_at DESC`
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("memory batch_get: %w", err)
	}
	defer rows.Close()
	var chunks []Chunk
	for rows.Next() {
		var c Chunk
		var tagStr string
		if err := rows.Scan(&c.ID, &c.PeerID, &c.Content, &c.CreatedAt, &tagStr, &c.Category); err != nil {
			continue
		}
		if tagStr != "" {
			c.Tags = strings.Split(tagStr, ",")
		}
		chunks = append(chunks, c)
	}
	return chunks, rows.Err()
}

// MemoryStats holds aggregate information about the memory store.
type MemoryStats struct {
	TotalChunks  int            `json:"total_chunks"`
	ByCategory   map[string]int `json:"by_category"`
	MilvusActive bool           `json:"milvus_active"`
	MilvusCount  int            `json:"milvus_count"`
}

// Stats returns aggregate information about the peer's memory store.
func (s *Store) Stats(ctx context.Context, peerID string) (*MemoryStats, error) {
	stats := &MemoryStats{ByCategory: make(map[string]int)}
	err := s.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM chunks WHERE peer_id = ?`, peerID).Scan(&stats.TotalChunks)
	if err != nil {
		return nil, fmt.Errorf("memory stats: %w", err)
	}
	rows, err := s.db.QueryContext(ctx,
		`SELECT COALESCE(NULLIF(category,''),'uncategorized'), COUNT(*)
		   FROM chunks WHERE peer_id = ? GROUP BY category`, peerID)
	if err != nil {
		return nil, fmt.Errorf("memory stats categories: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var cat string
		var count int
		if err := rows.Scan(&cat, &count); err == nil {
			stats.ByCategory[cat] = count
		}
	}
	stats.MilvusActive = s.milvus != nil
	if s.milvus != nil {
		if n, err := s.milvus.Count(ctx); err == nil {
			stats.MilvusCount = n
		}
	}
	return stats, nil
}

// TagChunk updates the tags and/or category of an existing chunk.
func (s *Store) TagChunk(ctx context.Context, peerID, chunkID string, tags []string, category string) error {
	var owner string
	err := s.db.QueryRowContext(ctx, `SELECT peer_id FROM chunks WHERE id = ?`, chunkID).Scan(&owner)
	if err == sql.ErrNoRows {
		return fmt.Errorf("chunk %s not found", chunkID)
	}
	if err != nil {
		return fmt.Errorf("memory tag: lookup: %w", err)
	}
	if owner != peerID {
		return fmt.Errorf("chunk %s does not belong to peer", chunkID)
	}
	tagStr := strings.Join(tags, ",")
	_, err = s.db.ExecContext(ctx,
		`UPDATE chunks SET tags = ?, category = ? WHERE id = ?`,
		tagStr, category, chunkID)
	if err != nil {
		return fmt.Errorf("memory tag: update: %w", err)
	}
	return nil
}

// milvusSearch queries Milvus for semantically similar content.
func (s *Store) milvusSearch(ctx context.Context, peerID, query string, topK int) ([]SearchResult, error) {
	emb, err := s.embedder.Embed(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("milvus search: embed query: %w", err)
	}
	results, err := s.milvus.Query(ctx, emb, topK, peerID)
	if err != nil {
		return nil, err
	}
	out := make([]SearchResult, len(results))
	for i, r := range results {
		out[i] = SearchResult{
			ID:      r.ID,
			Content: r.Document,
			Score:   float64(r.Score),
		}
		if pid, ok := r.Metadata["peer_id"].(string); ok {
			out[i].PeerID = pid
		}
	}
	return out, nil
}

// indexMilvus stores a chunk's content and embedding in Milvus.
func (s *Store) indexMilvus(id, peerID, content string, tags []string, category string) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	emb, err := s.embedder.Embed(ctx, content)
	if err != nil {
		slog.Warn("memory: milvus embed failed", "chunk", id, "err", err)
		return
	}
	meta := map[string]any{
		"peer_id":  peerID,
		"category": category,
	}
	if len(tags) > 0 {
		meta["tags"] = strings.Join(tags, ",")
	}
	if err := s.milvus.Upsert(ctx, []string{id}, []string{content}, [][]float32{emb}, []map[string]any{meta}); err != nil {
		slog.Warn("memory: milvus upsert failed", "chunk", id, "err", err)
	}
}

// mergeResults merges BM25 and Milvus results, deduplicating by ID.
// BM25 and Milvus scores are on different scales, so we normalize
// by rank position: top BM25 result gets 1.0, top Milvus result gets 1.0,
// then merge and sort.
func mergeResults(bm25, milvusRes []SearchResult, topK int) []SearchResult {
	seen := make(map[string]SearchResult, len(bm25)+len(milvusRes))
	for i, r := range bm25 {
		r.Score = 1.0 - float64(i)*0.05 // rank-based score
		seen[r.ID] = r
	}
	for i, r := range milvusRes {
		milvusScore := 1.0 - float64(i)*0.05
		if existing, ok := seen[r.ID]; ok {
			// Boost score for results found in both.
			existing.Score = (existing.Score + milvusScore) / 2 * 1.2
			seen[r.ID] = existing
		} else {
			r.Score = milvusScore
			seen[r.ID] = r
		}
	}
	merged := make([]SearchResult, 0, len(seen))
	for _, r := range seen {
		merged = append(merged, r)
	}
	sort.Slice(merged, func(i, j int) bool {
		return merged[i].Score > merged[j].Score
	})
	if len(merged) > topK {
		merged = merged[:topK]
	}
	return merged
}

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
`)
	if err != nil {
		return err
	}
	// Best-effort migration for existing databases that lack the new columns.
	_, _ = db.Exec(`ALTER TABLE chunks ADD COLUMN tags TEXT NOT NULL DEFAULT ''`)
	_, _ = db.Exec(`ALTER TABLE chunks ADD COLUMN category TEXT NOT NULL DEFAULT ''`)
	return nil
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
