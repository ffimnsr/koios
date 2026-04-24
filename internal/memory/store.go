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
	"unicode"

	"github.com/ffimnsr/koios/internal/memory/milvus"
	"github.com/ffimnsr/koios/internal/redact"
	_ "modernc.org/sqlite" // register "sqlite" driver
)

// SearchResult is a single memory chunk returned by a search query.
type SearchResult struct {
	ID             string
	PeerID         string
	Content        string
	Tags           []string
	Category       string
	RetentionClass RetentionClass
	ExposurePolicy ExposurePolicy
	ExpiresAt      int64
	Score          float64 // BM25 rank (negative) or cosine similarity [0,1]
}

type RetentionClass string

const (
	RetentionClassWorking RetentionClass = "working"
	RetentionClassPinned  RetentionClass = "pinned"
	RetentionClassArchive RetentionClass = "archive"
)

type ExposurePolicy string

const (
	ExposurePolicyAuto       ExposurePolicy = "auto"
	ExposurePolicySearchOnly ExposurePolicy = "search_only"
)

type CandidateStatus string

const (
	CandidateStatusPending  CandidateStatus = "pending"
	CandidateStatusApproved CandidateStatus = "approved"
	CandidateStatusMerged   CandidateStatus = "merged"
	CandidateStatusRejected CandidateStatus = "rejected"
	CandidateStatusAll      CandidateStatus = "all"
)

const (
	CandidateCaptureManual          = "manual"
	CandidateCaptureAutoTurnExtract = "auto_turn_extract"
)

type ChunkOptions struct {
	Tags           []string
	Category       string
	RetentionClass RetentionClass
	ExposurePolicy ExposurePolicy
	ExpiresAt      int64
}

type CandidateProvenance struct {
	CaptureKind      string
	SourceSessionKey string
	SourceExcerpt    string
}

type Candidate struct {
	ID               string          `json:"id"`
	PeerID           string          `json:"peer_id"`
	Content          string          `json:"content"`
	CreatedAt        int64           `json:"created_at"`
	Tags             []string        `json:"tags,omitempty"`
	Category         string          `json:"category,omitempty"`
	RetentionClass   RetentionClass  `json:"retention_class,omitempty"`
	ExposurePolicy   ExposurePolicy  `json:"exposure_policy,omitempty"`
	ExpiresAt        int64           `json:"expires_at,omitempty"`
	Status           CandidateStatus `json:"status"`
	ReviewReason     string          `json:"review_reason,omitempty"`
	ReviewedAt       int64           `json:"reviewed_at,omitempty"`
	ResultChunkID    string          `json:"result_chunk_id,omitempty"`
	MergeIntoID      string          `json:"merge_into_id,omitempty"`
	CaptureKind      string          `json:"capture_kind,omitempty"`
	SourceSessionKey string          `json:"source_session_key,omitempty"`
	SourceExcerpt    string          `json:"source_excerpt,omitempty"`
}

type CandidatePatch struct {
	Content        *string
	Tags           *[]string
	Category       *string
	RetentionClass *RetentionClass
	ExposurePolicy *ExposurePolicy
	ExpiresAt      *int64
}

type EntityKind string

const (
	EntityKindPerson  EntityKind = "person"
	EntityKindProject EntityKind = "project"
	EntityKindPlace   EntityKind = "place"
	EntityKindTopic   EntityKind = "topic"
)

type Entity struct {
	ID               string     `json:"id"`
	PeerID           string     `json:"peer_id"`
	Kind             EntityKind `json:"kind"`
	Name             string     `json:"name"`
	Aliases          []string   `json:"aliases,omitempty"`
	Notes            string     `json:"notes,omitempty"`
	CreatedAt        int64      `json:"created_at"`
	UpdatedAt        int64      `json:"updated_at"`
	LastSeenAt       int64      `json:"last_seen_at,omitempty"`
	LinkedChunkCount int        `json:"linked_chunk_count,omitempty"`
}

type EntityPatch struct {
	Kind       *EntityKind
	Name       *string
	Aliases    *[]string
	Notes      *string
	LastSeenAt *int64
}

type EntityRelationship struct {
	ID             string `json:"id"`
	PeerID         string `json:"peer_id"`
	SourceEntityID string `json:"source_entity_id"`
	TargetEntityID string `json:"target_entity_id"`
	Relation       string `json:"relation"`
	Notes          string `json:"notes,omitempty"`
	CreatedAt      int64  `json:"created_at"`
	UpdatedAt      int64  `json:"updated_at"`
	SourceName     string `json:"source_name,omitempty"`
	TargetName     string `json:"target_name,omitempty"`
}

type EntityGraph struct {
	Entity       Entity               `json:"entity"`
	LinkedChunks []Chunk              `json:"linked_chunks,omitempty"`
	Outgoing     []EntityRelationship `json:"outgoing_relationships,omitempty"`
	Incoming     []EntityRelationship `json:"incoming_relationships,omitempty"`
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
	return s.InsertChunkWithOptions(ctx, peerID, content, ChunkOptions{Tags: tags, Category: category})
}

// InsertChunkWithOptions adds a content chunk with optional metadata.
func (s *Store) InsertChunkWithOptions(ctx context.Context, peerID, content string, opts ChunkOptions) (*Chunk, error) {
	normalized, err := normalizeChunkOptions(opts)
	if err != nil {
		return nil, err
	}
	content = redact.String(content)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("memory insert: begin tx: %w", err)
	}
	defer tx.Rollback()
	chunk, err := insertChunkTx(ctx, tx, peerID, content, normalized, time.Now().Unix())
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("memory insert: commit: %w", err)
	}
	s.reindexChunkAsync(*chunk)
	return chunk, nil
}

// Search returns the top-K chunks matching query for the given peer.
// When Milvus is available, results from both FTS5 and Milvus are merged.
// Otherwise, BM25 candidates are optionally reranked by cosine similarity.
func (s *Store) Search(ctx context.Context, peerID, query string, topK int) ([]SearchResult, error) {
	return s.search(ctx, peerID, query, topK, false)
}

// SearchForInjection returns the top-K chunks eligible for automatic prompt injection.
func (s *Store) SearchForInjection(ctx context.Context, peerID, query string, topK int) ([]SearchResult, error) {
	return s.search(ctx, peerID, query, topK, true)
}

func (s *Store) search(ctx context.Context, peerID, query string, topK int, injectionOnly bool) ([]SearchResult, error) {
	if query = strings.TrimSpace(sanitizeFTSQuery(query)); query == "" {
		return nil, nil
	}
	if err := s.purgeExpired(ctx); err != nil {
		return nil, err
	}
	candidates, err := s.bm25Search(ctx, peerID, query, topK*4, injectionOnly)
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
	ID             string         `json:"id"`
	PeerID         string         `json:"peer_id"`
	Content        string         `json:"content"`
	CreatedAt      int64          `json:"created_at"`
	Tags           []string       `json:"tags,omitempty"`
	Category       string         `json:"category,omitempty"`
	RetentionClass RetentionClass `json:"retention_class,omitempty"`
	ExposurePolicy ExposurePolicy `json:"exposure_policy,omitempty"`
	ExpiresAt      int64          `json:"expires_at,omitempty"`
}

// List returns all chunks stored for a peer, ordered by creation time descending.
func (s *Store) List(ctx context.Context, peerID string, limit int) ([]Chunk, error) {
	if limit <= 0 {
		limit = 100
	}
	if err := s.purgeExpired(ctx); err != nil {
		return nil, err
	}
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, peer_id, content, created_at, COALESCE(tags,''), COALESCE(category,''), COALESCE(retention_class,?), COALESCE(exposure_policy,?), COALESCE(expires_at,0) FROM chunks WHERE peer_id = ? ORDER BY created_at DESC LIMIT ?`,
		RetentionClassWorking, ExposurePolicyAuto,
		peerID, limit)
	if err != nil {
		return nil, fmt.Errorf("memory list: %w", err)
	}
	defer rows.Close()
	var chunks []Chunk
	for rows.Next() {
		c, err := scanChunk(rows)
		if err != nil {
			continue
		}
		chunks = append(chunks, c)
	}
	return chunks, rows.Err()
}

// Recent returns the N most-recently created chunks for a peer in
// chronological order (oldest first). Used by the LCM sliding-window injector.
func (s *Store) Recent(ctx context.Context, peerID string, n int) ([]Chunk, error) {
	return s.recent(ctx, peerID, n, false)
}

// RecentForInjection returns the most recent auto-injectable chunks for a peer.
func (s *Store) RecentForInjection(ctx context.Context, peerID string, n int) ([]Chunk, error) {
	return s.recent(ctx, peerID, n, true)
}

func (s *Store) recent(ctx context.Context, peerID string, n int, injectionOnly bool) ([]Chunk, error) {
	if n <= 0 {
		return nil, nil
	}
	if err := s.purgeExpired(ctx); err != nil {
		return nil, err
	}
	query := recentChunksQuery
	if injectionOnly {
		query = recentInjectableChunksQuery
	}
	rows, err := s.db.QueryContext(ctx, query, peerID, n)
	if err != nil {
		return nil, fmt.Errorf("memory recent: %w", err)
	}
	defer rows.Close()
	var chunks []Chunk
	for rows.Next() {
		c, err := scanChunk(rows)
		if err != nil {
			continue
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
	if err := s.purgeExpired(ctx); err != nil {
		return nil, err
	}
	var c Chunk
	var tagStr string
	err := s.db.QueryRowContext(ctx,
		`SELECT id, peer_id, content, created_at, COALESCE(tags,''), COALESCE(category,''), COALESCE(retention_class,?), COALESCE(exposure_policy,?), COALESCE(expires_at,0) FROM chunks WHERE id = ?`,
		RetentionClassWorking, ExposurePolicyAuto, chunkID,
	).Scan(&c.ID, &c.PeerID, &c.Content, &c.CreatedAt, &tagStr, &c.Category, &c.RetentionClass, &c.ExposurePolicy, &c.ExpiresAt)
	if err == sql.ErrNoRows {
		return nil, fmt.Errorf("chunk %s not found", chunkID)
	}
	if err != nil {
		return nil, fmt.Errorf("memory get: %w", err)
	}
	if c.PeerID != peerID {
		return nil, fmt.Errorf("chunk %s does not belong to peer", chunkID)
	}
	c.Tags = parseTags(tagStr)
	return &c, nil
}

// DeleteChunk removes a single memory chunk by ID, enforcing peer ownership.
func (s *Store) DeleteChunk(ctx context.Context, peerID, chunkID string) error {
	if err := s.purgeExpired(ctx); err != nil {
		return err
	}
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
	if err := s.purgeExpired(ctx); err != nil {
		return err
	}
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
	if _, err := tx.ExecContext(ctx, `DELETE FROM memory_candidates WHERE peer_id = ?`, peerID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM memory_entity_edges WHERE peer_id = ?`, peerID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM memory_entity_chunks WHERE entity_id IN (SELECT id FROM memory_entities WHERE peer_id = ?)`, peerID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM memory_entities_fts WHERE peer_id = ?`, peerID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM memory_entities WHERE peer_id = ?`, peerID); err != nil {
		return err
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
	if err := s.purgeExpired(ctx); err != nil {
		return nil, err
	}
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
	rows, err := s.db.QueryContext(ctx, timelineBeforeQuery, peerID, anchorTime, depthBefore)
	if err != nil {
		return nil, fmt.Errorf("memory timeline before: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		c, err := scanChunk(rows)
		if err != nil {
			continue
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
	rows2, err := s.db.QueryContext(ctx, timelineAfterQuery, peerID, anchorTime, depthAfter)
	if err != nil {
		return nil, fmt.Errorf("memory timeline after: %w", err)
	}
	defer rows2.Close()
	for rows2.Next() {
		c, err := scanChunk(rows2)
		if err != nil {
			continue
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
	if err := s.purgeExpired(ctx); err != nil {
		return nil, err
	}
	placeholders := make([]string, len(ids))
	args := make([]any, 0, len(ids)+1)
	args = append(args, peerID)
	for i, id := range ids {
		placeholders[i] = "?"
		args = append(args, id)
	}
	query := fmt.Sprintf(batchGetQueryTemplate, strings.Join(placeholders, ","))
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("memory batch_get: %w", err)
	}
	defer rows.Close()
	var chunks []Chunk
	for rows.Next() {
		c, err := scanChunk(rows)
		if err != nil {
			continue
		}
		chunks = append(chunks, c)
	}
	return chunks, rows.Err()
}

// MemoryStats holds aggregate information about the memory store.
type MemoryStats struct {
	TotalChunks  int                    `json:"total_chunks"`
	ByCategory   map[string]int         `json:"by_category"`
	ByRetention  map[RetentionClass]int `json:"by_retention"`
	MilvusActive bool                   `json:"milvus_active"`
	MilvusCount  int                    `json:"milvus_count"`
}

// Stats returns aggregate information about the peer's memory store.
func (s *Store) Stats(ctx context.Context, peerID string) (*MemoryStats, error) {
	if err := s.purgeExpired(ctx); err != nil {
		return nil, err
	}
	stats := &MemoryStats{ByCategory: make(map[string]int), ByRetention: make(map[RetentionClass]int)}
	err := s.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM chunks WHERE peer_id = ?`, peerID).Scan(&stats.TotalChunks)
	if err != nil {
		return nil, fmt.Errorf("memory stats: %w", err)
	}
	rows, err := s.db.QueryContext(ctx, statsByCategoryQuery, peerID)
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
	retentionRows, err := s.db.QueryContext(ctx, statsByRetentionQuery, peerID)
	if err != nil {
		return nil, fmt.Errorf("memory stats retention: %w", err)
	}
	defer retentionRows.Close()
	for retentionRows.Next() {
		var retention string
		var count int
		if err := retentionRows.Scan(&retention, &count); err == nil {
			stats.ByRetention[RetentionClass(retention)] = count
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

// TagChunk updates the metadata of an existing chunk.
func (s *Store) TagChunk(ctx context.Context, peerID, chunkID string, tags []string, category string, retentionClass RetentionClass, exposurePolicy ExposurePolicy, expiresAt int64) error {
	if err := s.purgeExpired(ctx); err != nil {
		return err
	}
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
	normalized, err := normalizeChunkOptions(ChunkOptions{
		Tags:           tags,
		Category:       category,
		RetentionClass: retentionClass,
		ExposurePolicy: exposurePolicy,
		ExpiresAt:      expiresAt,
	})
	if err != nil {
		return err
	}
	tagStr := strings.Join(normalized.Tags, ",")
	_, err = s.db.ExecContext(ctx,
		`UPDATE chunks SET tags = ?, category = ?, retention_class = ?, exposure_policy = ?, expires_at = ? WHERE id = ?`,
		tagStr, normalized.Category, normalized.RetentionClass, normalized.ExposurePolicy, normalized.ExpiresAt, chunkID)
	if err != nil {
		return fmt.Errorf("memory tag: update: %w", err)
	}
	return nil
}

// QueueCandidate stores a memory candidate for explicit review before it is
// promoted into durable memory.
func (s *Store) QueueCandidate(ctx context.Context, peerID, content string, opts ChunkOptions) (*Candidate, error) {
	return s.QueueCandidateWithProvenance(ctx, peerID, content, opts, CandidateProvenance{})
}

// QueueCandidateWithProvenance stores a memory candidate with source metadata
// so review surfaces can explain where the candidate came from.
func (s *Store) QueueCandidateWithProvenance(ctx context.Context, peerID, content string, opts ChunkOptions, provenance CandidateProvenance) (*Candidate, error) {
	normalized, err := normalizeChunkOptions(opts)
	if err != nil {
		return nil, err
	}
	provenance, err = normalizeCandidateProvenance(provenance)
	if err != nil {
		return nil, err
	}
	content = strings.TrimSpace(redact.String(content))
	if content == "" {
		return nil, fmt.Errorf("content is required")
	}
	candidate := &Candidate{
		ID:               newID(),
		PeerID:           peerID,
		Content:          content,
		CreatedAt:        time.Now().Unix(),
		Tags:             normalized.Tags,
		Category:         normalized.Category,
		RetentionClass:   normalized.RetentionClass,
		ExposurePolicy:   normalized.ExposurePolicy,
		ExpiresAt:        normalized.ExpiresAt,
		Status:           CandidateStatusPending,
		CaptureKind:      provenance.CaptureKind,
		SourceSessionKey: provenance.SourceSessionKey,
		SourceExcerpt:    provenance.SourceExcerpt,
	}
	if _, err := s.db.ExecContext(ctx,
		`INSERT INTO memory_candidates(id, peer_id, content, created_at, tags, category, retention_class, exposure_policy, expires_at, status, review_reason, reviewed_at, result_chunk_id, merge_into_id, capture_kind, source_session_key, source_excerpt) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		candidate.ID, candidate.PeerID, candidate.Content, candidate.CreatedAt, strings.Join(candidate.Tags, ","), candidate.Category, candidate.RetentionClass, candidate.ExposurePolicy, candidate.ExpiresAt, candidate.Status, "", 0, "", "", candidate.CaptureKind, candidate.SourceSessionKey, candidate.SourceExcerpt); err != nil {
		return nil, fmt.Errorf("memory candidate create: %w", err)
	}
	return candidate, nil
}

// ListCandidates returns candidate memories for a peer ordered newest-first.
func (s *Store) ListCandidates(ctx context.Context, peerID string, limit int, status CandidateStatus) ([]Candidate, error) {
	status, err := normalizeCandidateStatusFilter(status)
	if err != nil {
		return nil, err
	}
	if limit <= 0 {
		limit = 50
	}
	var (
		rows *sql.Rows
	)
	if status == CandidateStatusAll {
		rows, err = s.db.QueryContext(ctx,
			`SELECT id, peer_id, content, created_at, COALESCE(tags,''), COALESCE(category,''), COALESCE(retention_class,?), COALESCE(exposure_policy,?), COALESCE(expires_at,0), COALESCE(status,?), COALESCE(review_reason,''), COALESCE(reviewed_at,0), COALESCE(result_chunk_id,''), COALESCE(merge_into_id,''), COALESCE(capture_kind,''), COALESCE(source_session_key,''), COALESCE(source_excerpt,'') FROM memory_candidates WHERE peer_id = ? ORDER BY created_at DESC LIMIT ?`,
			RetentionClassWorking, ExposurePolicyAuto, CandidateStatusPending, peerID, limit)
	} else {
		rows, err = s.db.QueryContext(ctx,
			`SELECT id, peer_id, content, created_at, COALESCE(tags,''), COALESCE(category,''), COALESCE(retention_class,?), COALESCE(exposure_policy,?), COALESCE(expires_at,0), COALESCE(status,?), COALESCE(review_reason,''), COALESCE(reviewed_at,0), COALESCE(result_chunk_id,''), COALESCE(merge_into_id,''), COALESCE(capture_kind,''), COALESCE(source_session_key,''), COALESCE(source_excerpt,'') FROM memory_candidates WHERE peer_id = ? AND status = ? ORDER BY created_at DESC LIMIT ?`,
			RetentionClassWorking, ExposurePolicyAuto, CandidateStatusPending, peerID, status, limit)
	}
	if err != nil {
		return nil, fmt.Errorf("memory candidate list: %w", err)
	}
	defer rows.Close()
	var candidates []Candidate
	for rows.Next() {
		candidate, err := scanCandidate(rows)
		if err != nil {
			continue
		}
		candidates = append(candidates, candidate)
	}
	return candidates, rows.Err()
}

// EditCandidate updates a pending memory candidate in place.
func (s *Store) EditCandidate(ctx context.Context, peerID, candidateID string, patch CandidatePatch) (*Candidate, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("memory candidate edit: begin tx: %w", err)
	}
	defer tx.Rollback()
	candidate, err := loadCandidateTx(ctx, tx, peerID, candidateID)
	if err != nil {
		return nil, err
	}
	if candidate.Status != CandidateStatusPending {
		return nil, fmt.Errorf("candidate %s is already reviewed", candidateID)
	}
	content, opts, err := applyCandidatePatch(*candidate, patch)
	if err != nil {
		return nil, err
	}
	candidate.Content = content
	candidate.Tags = opts.Tags
	candidate.Category = opts.Category
	candidate.RetentionClass = opts.RetentionClass
	candidate.ExposurePolicy = opts.ExposurePolicy
	candidate.ExpiresAt = opts.ExpiresAt
	if err := updateCandidateTx(ctx, tx, *candidate); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("memory candidate edit: commit: %w", err)
	}
	return candidate, nil
}

// ApproveCandidate promotes a pending candidate into durable memory.
func (s *Store) ApproveCandidate(ctx context.Context, peerID, candidateID string, patch CandidatePatch, reason string) (*Candidate, *Chunk, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, nil, fmt.Errorf("memory candidate approve: begin tx: %w", err)
	}
	defer tx.Rollback()
	candidate, err := loadCandidateTx(ctx, tx, peerID, candidateID)
	if err != nil {
		return nil, nil, err
	}
	if candidate.Status != CandidateStatusPending {
		return nil, nil, fmt.Errorf("candidate %s is already reviewed", candidateID)
	}
	content, opts, err := applyCandidatePatch(*candidate, patch)
	if err != nil {
		return nil, nil, err
	}
	chunk, err := insertChunkTx(ctx, tx, peerID, content, opts, time.Now().Unix())
	if err != nil {
		return nil, nil, err
	}
	candidate.Content = content
	candidate.Tags = opts.Tags
	candidate.Category = opts.Category
	candidate.RetentionClass = opts.RetentionClass
	candidate.ExposurePolicy = opts.ExposurePolicy
	candidate.ExpiresAt = opts.ExpiresAt
	candidate.Status = CandidateStatusApproved
	candidate.ReviewReason = strings.TrimSpace(reason)
	candidate.ReviewedAt = time.Now().Unix()
	candidate.ResultChunkID = chunk.ID
	candidate.MergeIntoID = ""
	if err := updateCandidateTx(ctx, tx, *candidate); err != nil {
		return nil, nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, nil, fmt.Errorf("memory candidate approve: commit: %w", err)
	}
	s.reindexChunkAsync(*chunk)
	return candidate, chunk, nil
}

// MergeCandidate appends a candidate into an existing memory record.
func (s *Store) MergeCandidate(ctx context.Context, peerID, candidateID, targetChunkID string, patch CandidatePatch, reason string) (*Candidate, *Chunk, error) {
	if strings.TrimSpace(targetChunkID) == "" {
		return nil, nil, fmt.Errorf("merge_into_id is required")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, nil, fmt.Errorf("memory candidate merge: begin tx: %w", err)
	}
	defer tx.Rollback()
	candidate, err := loadCandidateTx(ctx, tx, peerID, candidateID)
	if err != nil {
		return nil, nil, err
	}
	if candidate.Status != CandidateStatusPending {
		return nil, nil, fmt.Errorf("candidate %s is already reviewed", candidateID)
	}
	content, opts, err := applyCandidatePatch(*candidate, patch)
	if err != nil {
		return nil, nil, err
	}
	target, err := loadChunkTx(ctx, tx, peerID, targetChunkID)
	if err != nil {
		return nil, nil, err
	}
	mergedContent := strings.TrimSpace(target.Content)
	if mergedContent == "" {
		mergedContent = content
	} else if content != "" {
		mergedContent = mergedContent + "\n\n" + content
	}
	target.Content = mergedContent
	target.Tags = compactTags(append(append([]string{}, target.Tags...), opts.Tags...))
	if target.Category == "" {
		target.Category = opts.Category
	}
	if err := updateChunkTx(ctx, tx, *target); err != nil {
		return nil, nil, err
	}
	candidate.Content = content
	candidate.Tags = opts.Tags
	candidate.Category = opts.Category
	candidate.RetentionClass = opts.RetentionClass
	candidate.ExposurePolicy = opts.ExposurePolicy
	candidate.ExpiresAt = opts.ExpiresAt
	candidate.Status = CandidateStatusMerged
	candidate.ReviewReason = strings.TrimSpace(reason)
	candidate.ReviewedAt = time.Now().Unix()
	candidate.ResultChunkID = target.ID
	candidate.MergeIntoID = target.ID
	if err := updateCandidateTx(ctx, tx, *candidate); err != nil {
		return nil, nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, nil, fmt.Errorf("memory candidate merge: commit: %w", err)
	}
	s.reindexChunkAsync(*target)
	return candidate, target, nil
}

// RejectCandidate removes a candidate from the review queue without creating durable memory.
func (s *Store) RejectCandidate(ctx context.Context, peerID, candidateID, reason string) (*Candidate, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("memory candidate reject: begin tx: %w", err)
	}
	defer tx.Rollback()
	candidate, err := loadCandidateTx(ctx, tx, peerID, candidateID)
	if err != nil {
		return nil, err
	}
	if candidate.Status != CandidateStatusPending {
		return nil, fmt.Errorf("candidate %s is already reviewed", candidateID)
	}
	candidate.Status = CandidateStatusRejected
	candidate.ReviewReason = strings.TrimSpace(reason)
	candidate.ReviewedAt = time.Now().Unix()
	candidate.ResultChunkID = ""
	candidate.MergeIntoID = ""
	if err := updateCandidateTx(ctx, tx, *candidate); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("memory candidate reject: commit: %w", err)
	}
	return candidate, nil
}

// CreateEntity inserts a durable entity record that can anchor future memory.
func (s *Store) CreateEntity(ctx context.Context, peerID string, kind EntityKind, name string, aliases []string, notes string, lastSeenAt int64) (*Entity, error) {
	entity, err := newEntity(peerID, kind, name, aliases, notes, lastSeenAt)
	if err != nil {
		return nil, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("memory entity create: begin tx: %w", err)
	}
	defer tx.Rollback()
	if err := insertEntityTx(ctx, tx, *entity); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("memory entity create: commit: %w", err)
	}
	return entity, nil
}

// UpsertEntity merges a newly observed entity into an existing durable record
// when the name or aliases already match for the same peer and kind.
func (s *Store) UpsertEntity(ctx context.Context, peerID string, kind EntityKind, name string, aliases []string, notes string, lastSeenAt int64) (*Entity, error) {
	normalizedKind, normalizedName, normalizedAliases, normalizedNotes, normalizedLastSeenAt, err := normalizeEntityFields(kind, name, aliases, notes, lastSeenAt)
	if err != nil {
		return nil, err
	}
	if normalizedLastSeenAt == 0 {
		normalizedLastSeenAt = time.Now().Unix()
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("memory entity upsert: begin tx: %w", err)
	}
	defer tx.Rollback()
	existing, err := findEntityByIdentityTx(ctx, tx, peerID, normalizedKind, normalizedName, normalizedAliases)
	if err != nil {
		return nil, err
	}
	if existing == nil {
		entity := Entity{
			ID:         newID(),
			PeerID:     peerID,
			Kind:       normalizedKind,
			Name:       normalizedName,
			Aliases:    normalizedAliases,
			Notes:      normalizedNotes,
			CreatedAt:  normalizedLastSeenAt,
			UpdatedAt:  normalizedLastSeenAt,
			LastSeenAt: normalizedLastSeenAt,
		}
		if entity.CreatedAt <= 0 {
			now := time.Now().Unix()
			entity.CreatedAt = now
			entity.UpdatedAt = now
			entity.LastSeenAt = maxInt64(entity.LastSeenAt, now)
		}
		if err := insertEntityTx(ctx, tx, entity); err != nil {
			return nil, err
		}
		if err := tx.Commit(); err != nil {
			return nil, fmt.Errorf("memory entity upsert: commit create: %w", err)
		}
		return &entity, nil
	}
	existing.Aliases = compactAliases(append(existing.Aliases, normalizedAliases...))
	if mergedNotes := mergeEntityNotes(existing.Notes, normalizedNotes); mergedNotes != existing.Notes {
		existing.Notes = mergedNotes
	}
	existing.LastSeenAt = maxInt64(existing.LastSeenAt, normalizedLastSeenAt)
	existing.UpdatedAt = time.Now().Unix()
	if err := updateEntityTx(ctx, tx, *existing); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("memory entity upsert: commit update: %w", err)
	}
	return existing, nil
}

// UpdateEntity edits a stored entity in place.
func (s *Store) UpdateEntity(ctx context.Context, peerID, entityID string, patch EntityPatch) (*Entity, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("memory entity update: begin tx: %w", err)
	}
	defer tx.Rollback()
	entity, err := loadEntityTx(ctx, tx, peerID, entityID)
	if err != nil {
		return nil, err
	}
	updated, err := applyEntityPatch(*entity, patch)
	if err != nil {
		return nil, err
	}
	if err := updateEntityTx(ctx, tx, updated); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("memory entity update: commit: %w", err)
	}
	return &updated, nil
}

// TouchEntity updates the last-seen timestamp for an entity.
func (s *Store) TouchEntity(ctx context.Context, peerID, entityID string, lastSeenAt int64) (*Entity, error) {
	if lastSeenAt <= 0 {
		lastSeenAt = time.Now().Unix()
	}
	return s.UpdateEntity(ctx, peerID, entityID, EntityPatch{LastSeenAt: &lastSeenAt})
}

// DeleteEntity removes a durable entity and any linked edges or chunk links.
func (s *Store) DeleteEntity(ctx context.Context, peerID, entityID string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("memory entity delete: begin tx: %w", err)
	}
	defer tx.Rollback()
	if _, err := loadEntityTx(ctx, tx, peerID, entityID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM memory_entities_fts WHERE id = ?`, entityID); err != nil {
		return fmt.Errorf("memory entity delete fts: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM memory_entities WHERE id = ? AND peer_id = ?`, entityID, peerID); err != nil {
		return fmt.Errorf("memory entity delete: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("memory entity delete: commit: %w", err)
	}
	return nil
}

// GetEntity returns one entity with counts but without expanded graph data.
func (s *Store) GetEntity(ctx context.Context, peerID, entityID string) (*Entity, error) {
	entity, err := scanEntity(s.db.QueryRowContext(ctx,
		`SELECT id, peer_id, kind, name, COALESCE(aliases,''), COALESCE(notes,''), created_at, updated_at, COALESCE(last_seen_at,0), (SELECT COUNT(*) FROM memory_entity_chunks WHERE entity_id = memory_entities.id) FROM memory_entities WHERE id = ?`,
		entityID))
	if err == sql.ErrNoRows {
		return nil, fmt.Errorf("entity %s not found", entityID)
	}
	if err != nil {
		return nil, fmt.Errorf("memory entity get: %w", err)
	}
	if entity.PeerID != peerID {
		return nil, fmt.Errorf("entity %s does not belong to peer", entityID)
	}
	return &entity, nil
}

// GetEntityGraph returns an entity plus linked chunks and adjacent edges.
func (s *Store) GetEntityGraph(ctx context.Context, peerID, entityID string) (*EntityGraph, error) {
	if err := s.purgeExpired(ctx); err != nil {
		return nil, err
	}
	entity, err := s.GetEntity(ctx, peerID, entityID)
	if err != nil {
		return nil, err
	}
	linkedChunks, err := s.listEntityChunks(ctx, peerID, entityID)
	if err != nil {
		return nil, err
	}
	outgoing, err := s.listEntityEdges(ctx, peerID, entityID, true)
	if err != nil {
		return nil, err
	}
	incoming, err := s.listEntityEdges(ctx, peerID, entityID, false)
	if err != nil {
		return nil, err
	}
	return &EntityGraph{
		Entity:       *entity,
		LinkedChunks: linkedChunks,
		Outgoing:     outgoing,
		Incoming:     incoming,
	}, nil
}

// ListEntities returns peer-scoped entities ordered by most recently seen.
func (s *Store) ListEntities(ctx context.Context, peerID string, kind EntityKind, limit int) ([]Entity, error) {
	if limit <= 0 {
		limit = 50
	}
	normalizedKind, err := normalizeEntityKind(kind, true)
	if err != nil {
		return nil, err
	}
	var rows *sql.Rows
	if normalizedKind == "" {
		rows, err = s.db.QueryContext(ctx,
			`SELECT id, peer_id, kind, name, COALESCE(aliases,''), COALESCE(notes,''), created_at, updated_at, COALESCE(last_seen_at,0), (SELECT COUNT(*) FROM memory_entity_chunks WHERE entity_id = memory_entities.id) FROM memory_entities WHERE peer_id = ? ORDER BY last_seen_at DESC, updated_at DESC, name ASC LIMIT ?`,
			peerID, limit)
	} else {
		rows, err = s.db.QueryContext(ctx,
			`SELECT id, peer_id, kind, name, COALESCE(aliases,''), COALESCE(notes,''), created_at, updated_at, COALESCE(last_seen_at,0), (SELECT COUNT(*) FROM memory_entity_chunks WHERE entity_id = memory_entities.id) FROM memory_entities WHERE peer_id = ? AND kind = ? ORDER BY last_seen_at DESC, updated_at DESC, name ASC LIMIT ?`,
			peerID, normalizedKind, limit)
	}
	if err != nil {
		return nil, fmt.Errorf("memory entity list: %w", err)
	}
	defer rows.Close()
	var entities []Entity
	for rows.Next() {
		entity, err := scanEntity(rows)
		if err != nil {
			continue
		}
		entities = append(entities, entity)
	}
	return entities, rows.Err()
}

// SearchEntities finds entities by name, aliases, or notes.
func (s *Store) SearchEntities(ctx context.Context, peerID, query string, limit int) ([]Entity, error) {
	query = strings.TrimSpace(sanitizeFTSQuery(query))
	if query == "" {
		return nil, nil
	}
	if limit <= 0 {
		limit = 10
	}
	rows, err := s.db.QueryContext(ctx, searchEntitiesQuery, query, peerID, limit)
	if err != nil {
		return nil, fmt.Errorf("memory entity search: %w", err)
	}
	defer rows.Close()
	var entities []Entity
	for rows.Next() {
		entity, err := scanEntity(rows)
		if err != nil {
			continue
		}
		entities = append(entities, entity)
	}
	return entities, rows.Err()
}

// LinkChunkToEntity associates a stored memory chunk with an entity.
func (s *Store) LinkChunkToEntity(ctx context.Context, peerID, entityID, chunkID string) error {
	if err := s.purgeExpired(ctx); err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("memory entity link chunk: begin tx: %w", err)
	}
	defer tx.Rollback()
	entity, err := loadEntityTx(ctx, tx, peerID, entityID)
	if err != nil {
		return err
	}
	if _, err := loadChunkTx(ctx, tx, peerID, chunkID); err != nil {
		return err
	}
	now := time.Now().Unix()
	if _, err := tx.ExecContext(ctx,
		`INSERT OR IGNORE INTO memory_entity_chunks(entity_id, chunk_id, created_at) VALUES (?,?,?)`,
		entity.ID, chunkID, now); err != nil {
		return fmt.Errorf("memory entity link chunk: %w", err)
	}
	entity.LastSeenAt = maxInt64(entity.LastSeenAt, now)
	entity.UpdatedAt = now
	if err := updateEntityTx(ctx, tx, *entity); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("memory entity link chunk: commit: %w", err)
	}
	return nil
}

// UnlinkChunkFromEntity removes an entity-to-chunk association.
func (s *Store) UnlinkChunkFromEntity(ctx context.Context, peerID, entityID, chunkID string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("memory entity unlink chunk: begin tx: %w", err)
	}
	defer tx.Rollback()
	entity, err := loadEntityTx(ctx, tx, peerID, entityID)
	if err != nil {
		return err
	}
	if _, err := loadChunkTx(ctx, tx, peerID, chunkID); err != nil {
		return err
	}
	result, err := tx.ExecContext(ctx,
		`DELETE FROM memory_entity_chunks WHERE entity_id = ? AND chunk_id = ?`,
		entity.ID, chunkID)
	if err != nil {
		return fmt.Errorf("memory entity unlink chunk: %w", err)
	}
	rowsAffected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("memory entity unlink chunk rows: %w", err)
	}
	if rowsAffected == 0 {
		return fmt.Errorf("chunk %s is not linked to entity %s", chunkID, entityID)
	}
	entity.UpdatedAt = time.Now().Unix()
	if err := updateEntityTx(ctx, tx, *entity); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("memory entity unlink chunk: commit: %w", err)
	}
	return nil
}

// RelateEntities creates or refreshes a peer-scoped relationship edge.
func (s *Store) RelateEntities(ctx context.Context, peerID, sourceEntityID, targetEntityID, relation, notes string) (*EntityRelationship, error) {
	if strings.TrimSpace(sourceEntityID) == "" || strings.TrimSpace(targetEntityID) == "" {
		return nil, fmt.Errorf("source_entity_id and target_entity_id are required")
	}
	if sourceEntityID == targetEntityID {
		return nil, fmt.Errorf("entity relationship requires distinct source and target")
	}
	relation, err := normalizeEntityRelation(relation)
	if err != nil {
		return nil, err
	}
	notes = strings.TrimSpace(redact.String(notes))
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("memory entity relate: begin tx: %w", err)
	}
	defer tx.Rollback()
	source, err := loadEntityTx(ctx, tx, peerID, sourceEntityID)
	if err != nil {
		return nil, err
	}
	target, err := loadEntityTx(ctx, tx, peerID, targetEntityID)
	if err != nil {
		return nil, err
	}
	now := time.Now().Unix()
	edgeID := newID()
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO memory_entity_edges(id, peer_id, source_entity_id, target_entity_id, relation, notes, created_at, updated_at) VALUES (?,?,?,?,?,?,?,?) ON CONFLICT(peer_id, source_entity_id, target_entity_id, relation) DO UPDATE SET notes = excluded.notes, updated_at = excluded.updated_at`,
		edgeID, peerID, source.ID, target.ID, relation, notes, now, now); err != nil {
		return nil, fmt.Errorf("memory entity relate: %w", err)
	}
	if _, err := tx.ExecContext(ctx,
		`UPDATE memory_entities SET last_seen_at = ?, updated_at = ? WHERE id IN (?, ?)`,
		now, now, source.ID, target.ID); err != nil {
		return nil, fmt.Errorf("memory entity relate touch: %w", err)
	}
	edge, err := loadEntityEdgeTx(ctx, tx, peerID, source.ID, target.ID, relation)
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("memory entity relate: commit: %w", err)
	}
	return edge, nil
}

// UnrelateEntities removes a relationship edge between two entities.
func (s *Store) UnrelateEntities(ctx context.Context, peerID, sourceEntityID, targetEntityID, relation string) error {
	if strings.TrimSpace(sourceEntityID) == "" || strings.TrimSpace(targetEntityID) == "" {
		return fmt.Errorf("source_entity_id and target_entity_id are required")
	}
	relation, err := normalizeEntityRelation(relation)
	if err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("memory entity unrelate: begin tx: %w", err)
	}
	defer tx.Rollback()
	if _, err := loadEntityTx(ctx, tx, peerID, sourceEntityID); err != nil {
		return err
	}
	if _, err := loadEntityTx(ctx, tx, peerID, targetEntityID); err != nil {
		return err
	}
	result, err := tx.ExecContext(ctx,
		`DELETE FROM memory_entity_edges WHERE peer_id = ? AND source_entity_id = ? AND target_entity_id = ? AND relation = ?`,
		peerID, sourceEntityID, targetEntityID, relation)
	if err != nil {
		return fmt.Errorf("memory entity unrelate: %w", err)
	}
	rowsAffected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("memory entity unrelate rows: %w", err)
	}
	if rowsAffected == 0 {
		return fmt.Errorf("entity relationship not found")
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("memory entity unrelate: commit: %w", err)
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
			ID:             r.ID,
			Content:        r.Document,
			RetentionClass: RetentionClassWorking,
			ExposurePolicy: ExposurePolicyAuto,
			Score:          float64(r.Score),
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

func insertChunkTx(ctx context.Context, tx *sql.Tx, peerID, content string, opts ChunkOptions, createdAt int64) (*Chunk, error) {
	chunk := &Chunk{
		ID:             newID(),
		PeerID:         peerID,
		Content:        content,
		CreatedAt:      createdAt,
		Tags:           opts.Tags,
		Category:       opts.Category,
		RetentionClass: opts.RetentionClass,
		ExposurePolicy: opts.ExposurePolicy,
		ExpiresAt:      opts.ExpiresAt,
	}
	tagStr := strings.Join(chunk.Tags, ",")
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO chunks(id, peer_id, content, created_at, tags, category, retention_class, exposure_policy, expires_at) VALUES (?,?,?,?,?,?,?,?,?)`,
		chunk.ID, chunk.PeerID, chunk.Content, chunk.CreatedAt, tagStr, chunk.Category, chunk.RetentionClass, chunk.ExposurePolicy, chunk.ExpiresAt); err != nil {
		return nil, fmt.Errorf("memory insert: chunks: %w", err)
	}
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO chunks_fts(id, peer_id, content) VALUES (?,?,?)`,
		chunk.ID, chunk.PeerID, chunk.Content); err != nil {
		return nil, fmt.Errorf("memory insert: fts: %w", err)
	}
	return chunk, nil
}

func updateChunkTx(ctx context.Context, tx *sql.Tx, chunk Chunk) error {
	tagStr := strings.Join(chunk.Tags, ",")
	if _, err := tx.ExecContext(ctx,
		`UPDATE chunks SET content = ?, tags = ?, category = ?, retention_class = ?, exposure_policy = ?, expires_at = ? WHERE id = ? AND peer_id = ?`,
		chunk.Content, tagStr, chunk.Category, chunk.RetentionClass, chunk.ExposurePolicy, chunk.ExpiresAt, chunk.ID, chunk.PeerID); err != nil {
		return fmt.Errorf("memory update: chunks: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM chunks_fts WHERE id = ?`, chunk.ID); err != nil {
		return fmt.Errorf("memory update: fts delete: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO chunks_fts(id, peer_id, content) VALUES (?,?,?)`, chunk.ID, chunk.PeerID, chunk.Content); err != nil {
		return fmt.Errorf("memory update: fts insert: %w", err)
	}
	return nil
}

func loadChunkTx(ctx context.Context, tx *sql.Tx, peerID, chunkID string) (*Chunk, error) {
	chunk, err := scanChunk(tx.QueryRowContext(ctx,
		`SELECT id, peer_id, content, created_at, COALESCE(tags,''), COALESCE(category,''), COALESCE(retention_class,?), COALESCE(exposure_policy,?), COALESCE(expires_at,0) FROM chunks WHERE id = ?`,
		RetentionClassWorking, ExposurePolicyAuto, chunkID))
	if err == sql.ErrNoRows {
		return nil, fmt.Errorf("chunk %s not found", chunkID)
	}
	if err != nil {
		return nil, fmt.Errorf("memory get: %w", err)
	}
	if chunk.PeerID != peerID {
		return nil, fmt.Errorf("chunk %s does not belong to peer", chunkID)
	}
	return &chunk, nil
}

func loadCandidateTx(ctx context.Context, tx *sql.Tx, peerID, candidateID string) (*Candidate, error) {
	candidate, err := scanCandidate(tx.QueryRowContext(ctx,
		`SELECT id, peer_id, content, created_at, COALESCE(tags,''), COALESCE(category,''), COALESCE(retention_class,?), COALESCE(exposure_policy,?), COALESCE(expires_at,0), COALESCE(status,?), COALESCE(review_reason,''), COALESCE(reviewed_at,0), COALESCE(result_chunk_id,''), COALESCE(merge_into_id,''), COALESCE(capture_kind,''), COALESCE(source_session_key,''), COALESCE(source_excerpt,'') FROM memory_candidates WHERE id = ?`,
		RetentionClassWorking, ExposurePolicyAuto, CandidateStatusPending, candidateID))
	if err == sql.ErrNoRows {
		return nil, fmt.Errorf("candidate %s not found", candidateID)
	}
	if err != nil {
		return nil, fmt.Errorf("memory candidate get: %w", err)
	}
	if candidate.PeerID != peerID {
		return nil, fmt.Errorf("candidate %s does not belong to peer", candidateID)
	}
	return &candidate, nil
}

func updateCandidateTx(ctx context.Context, tx *sql.Tx, candidate Candidate) error {
	_, err := tx.ExecContext(ctx,
		`UPDATE memory_candidates SET content = ?, tags = ?, category = ?, retention_class = ?, exposure_policy = ?, expires_at = ?, status = ?, review_reason = ?, reviewed_at = ?, result_chunk_id = ?, merge_into_id = ?, capture_kind = ?, source_session_key = ?, source_excerpt = ? WHERE id = ? AND peer_id = ?`,
		candidate.Content, strings.Join(candidate.Tags, ","), candidate.Category, candidate.RetentionClass, candidate.ExposurePolicy, candidate.ExpiresAt, candidate.Status, candidate.ReviewReason, candidate.ReviewedAt, candidate.ResultChunkID, candidate.MergeIntoID, candidate.CaptureKind, candidate.SourceSessionKey, candidate.SourceExcerpt, candidate.ID, candidate.PeerID)
	if err != nil {
		return fmt.Errorf("memory candidate update: %w", err)
	}
	return nil
}

func applyCandidatePatch(candidate Candidate, patch CandidatePatch) (string, ChunkOptions, error) {
	content := candidate.Content
	if patch.Content != nil {
		content = strings.TrimSpace(redact.String(*patch.Content))
	}
	if content == "" {
		return "", ChunkOptions{}, fmt.Errorf("content is required")
	}
	opts := ChunkOptions{
		Tags:           append([]string(nil), candidate.Tags...),
		Category:       candidate.Category,
		RetentionClass: candidate.RetentionClass,
		ExposurePolicy: candidate.ExposurePolicy,
		ExpiresAt:      candidate.ExpiresAt,
	}
	if patch.Tags != nil {
		opts.Tags = append([]string(nil), (*patch.Tags)...)
	}
	if patch.Category != nil {
		opts.Category = *patch.Category
	}
	if patch.RetentionClass != nil {
		opts.RetentionClass = *patch.RetentionClass
	}
	if patch.ExposurePolicy != nil {
		opts.ExposurePolicy = *patch.ExposurePolicy
	}
	if patch.ExpiresAt != nil {
		opts.ExpiresAt = *patch.ExpiresAt
	}
	normalized, err := normalizeChunkOptions(opts)
	if err != nil {
		return "", ChunkOptions{}, err
	}
	return content, normalized, nil
}

func normalizeCandidateStatusFilter(status CandidateStatus) (CandidateStatus, error) {
	switch CandidateStatus(strings.ToLower(strings.TrimSpace(string(status)))) {
	case "", CandidateStatusPending:
		return CandidateStatusPending, nil
	case CandidateStatusApproved:
		return CandidateStatusApproved, nil
	case CandidateStatusMerged:
		return CandidateStatusMerged, nil
	case CandidateStatusRejected:
		return CandidateStatusRejected, nil
	case CandidateStatusAll:
		return CandidateStatusAll, nil
	default:
		return "", fmt.Errorf("invalid candidate status %q", status)
	}
}

func (s *Store) reindexChunkAsync(chunk Chunk) {
	if s.embedder != nil {
		go s.indexEmbedding(chunk.ID, chunk.Content)
	}
	if s.milvus != nil && s.embedder != nil {
		go s.indexMilvus(chunk.ID, chunk.PeerID, chunk.Content, chunk.Tags, chunk.Category)
	}
}

func (s *Store) bm25Search(ctx context.Context, peerID, query string, limit int, injectionOnly bool) ([]SearchResult, error) {
	querySQL := bm25SearchQuery
	if injectionOnly {
		querySQL = bm25SearchInjectableQuery
	}
	rows, err := s.db.QueryContext(ctx, querySQL, query, peerID, limit)
	if err != nil {
		return nil, fmt.Errorf("memory bm25: %w", err)
	}
	defer rows.Close()
	var results []SearchResult
	for rows.Next() {
		var r SearchResult
		var tagStr string
		var rank float64
		if err := rows.Scan(&r.ID, &r.PeerID, &r.Content, &tagStr, &r.Category, &r.RetentionClass, &r.ExposurePolicy, &r.ExpiresAt, &rank); err != nil {
			continue
		}
		r.Tags = parseTags(tagStr)
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
	_, err := db.Exec(schemaSQL)
	if err != nil {
		return err
	}
	// Best-effort migration for existing databases that lack the new columns.
	_, _ = db.Exec(`ALTER TABLE chunks ADD COLUMN tags TEXT NOT NULL DEFAULT ''`)
	_, _ = db.Exec(`ALTER TABLE chunks ADD COLUMN category TEXT NOT NULL DEFAULT ''`)
	_, _ = db.Exec(`ALTER TABLE chunks ADD COLUMN retention_class TEXT NOT NULL DEFAULT 'working'`)
	_, _ = db.Exec(`ALTER TABLE chunks ADD COLUMN exposure_policy TEXT NOT NULL DEFAULT 'auto'`)
	_, _ = db.Exec(`ALTER TABLE chunks ADD COLUMN expires_at INTEGER NOT NULL DEFAULT 0`)
	_, _ = db.Exec(`ALTER TABLE memory_candidates ADD COLUMN capture_kind TEXT NOT NULL DEFAULT 'manual'`)
	_, _ = db.Exec(`ALTER TABLE memory_candidates ADD COLUMN source_session_key TEXT NOT NULL DEFAULT ''`)
	_, _ = db.Exec(`ALTER TABLE memory_candidates ADD COLUMN source_excerpt TEXT NOT NULL DEFAULT ''`)
	_, _ = db.Exec(`CREATE INDEX IF NOT EXISTS idx_memory_candidates_peer_status_created_at ON memory_candidates(peer_id, status, created_at DESC)`)
	_, _ = db.Exec(`CREATE INDEX IF NOT EXISTS idx_memory_entities_peer_kind_name ON memory_entities(peer_id, kind, name)`)
	_, _ = db.Exec(`CREATE UNIQUE INDEX IF NOT EXISTS idx_memory_entity_edges_unique ON memory_entity_edges(peer_id, source_entity_id, target_entity_id, relation)`)
	_, _ = db.Exec(`CREATE INDEX IF NOT EXISTS idx_memory_entity_edges_source ON memory_entity_edges(peer_id, source_entity_id, updated_at DESC)`)
	_, _ = db.Exec(`CREATE INDEX IF NOT EXISTS idx_memory_entity_edges_target ON memory_entity_edges(peer_id, target_entity_id, updated_at DESC)`)
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
		switch {
		case unicode.IsLetter(c), unicode.IsDigit(c), unicode.IsSpace(c):
			b.WriteRune(c)
		default:
			b.WriteByte(' ')
		}
	}
	return b.String()
}

func normalizeChunkOptions(opts ChunkOptions) (ChunkOptions, error) {
	normalized := opts
	normalized.Category = strings.TrimSpace(normalized.Category)
	normalized.Tags = compactTags(normalized.Tags)
	switch normalized.RetentionClass {
	case "":
		normalized.RetentionClass = RetentionClassWorking
	case RetentionClassWorking, RetentionClassPinned, RetentionClassArchive:
	default:
		return ChunkOptions{}, fmt.Errorf("invalid retention_class %q", normalized.RetentionClass)
	}
	if normalized.ExposurePolicy == "" {
		if normalized.RetentionClass == RetentionClassArchive {
			normalized.ExposurePolicy = ExposurePolicySearchOnly
		} else {
			normalized.ExposurePolicy = ExposurePolicyAuto
		}
	}
	switch normalized.ExposurePolicy {
	case ExposurePolicyAuto, ExposurePolicySearchOnly:
	default:
		return ChunkOptions{}, fmt.Errorf("invalid exposure_policy %q", normalized.ExposurePolicy)
	}
	if normalized.RetentionClass == RetentionClassArchive && normalized.ExposurePolicy == ExposurePolicyAuto {
		return ChunkOptions{}, fmt.Errorf("archived memory cannot use exposure_policy %q", normalized.ExposurePolicy)
	}
	if normalized.ExpiresAt < 0 {
		return ChunkOptions{}, fmt.Errorf("expires_at must be >= 0")
	}
	return normalized, nil
}

func compactTags(tags []string) []string {
	if len(tags) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(tags))
	cleaned := make([]string, 0, len(tags))
	for _, tag := range tags {
		tag = strings.TrimSpace(tag)
		if tag == "" {
			continue
		}
		if _, ok := seen[tag]; ok {
			continue
		}
		seen[tag] = struct{}{}
		cleaned = append(cleaned, tag)
	}
	if len(cleaned) == 0 {
		return nil
	}
	return cleaned
}

func compactAliases(aliases []string) []string {
	return compactTags(aliases)
}

func parseTags(tagStr string) []string {
	if tagStr == "" {
		return nil
	}
	return strings.Split(tagStr, ",")
}

func parseAliases(aliasStr string) []string {
	return parseTags(aliasStr)
}

type rowScanner interface {
	Scan(dest ...any) error
}

func scanChunk(scanner rowScanner) (Chunk, error) {
	var c Chunk
	var tagStr string
	err := scanner.Scan(&c.ID, &c.PeerID, &c.Content, &c.CreatedAt, &tagStr, &c.Category, &c.RetentionClass, &c.ExposurePolicy, &c.ExpiresAt)
	if err != nil {
		return Chunk{}, err
	}
	c.Tags = parseTags(tagStr)
	return c, nil
}

func scanCandidate(scanner rowScanner) (Candidate, error) {
	var c Candidate
	var tagStr string
	err := scanner.Scan(&c.ID, &c.PeerID, &c.Content, &c.CreatedAt, &tagStr, &c.Category, &c.RetentionClass, &c.ExposurePolicy, &c.ExpiresAt, &c.Status, &c.ReviewReason, &c.ReviewedAt, &c.ResultChunkID, &c.MergeIntoID, &c.CaptureKind, &c.SourceSessionKey, &c.SourceExcerpt)
	if err != nil {
		return Candidate{}, err
	}
	c.Tags = parseTags(tagStr)
	if c.CaptureKind == "" {
		c.CaptureKind = CandidateCaptureManual
	}
	return c, nil
}

func scanEntity(scanner rowScanner) (Entity, error) {
	var entity Entity
	var aliasStr string
	err := scanner.Scan(&entity.ID, &entity.PeerID, &entity.Kind, &entity.Name, &aliasStr, &entity.Notes, &entity.CreatedAt, &entity.UpdatedAt, &entity.LastSeenAt, &entity.LinkedChunkCount)
	if err != nil {
		return Entity{}, err
	}
	entity.Aliases = parseAliases(aliasStr)
	return entity, nil
}

func scanEntityRelationship(scanner rowScanner) (EntityRelationship, error) {
	var rel EntityRelationship
	err := scanner.Scan(&rel.ID, &rel.PeerID, &rel.SourceEntityID, &rel.TargetEntityID, &rel.Relation, &rel.Notes, &rel.CreatedAt, &rel.UpdatedAt, &rel.SourceName, &rel.TargetName)
	if err != nil {
		return EntityRelationship{}, err
	}
	return rel, nil
}

func normalizeEntityKind(kind EntityKind, allowEmpty bool) (EntityKind, error) {
	kind = EntityKind(strings.ToLower(strings.TrimSpace(string(kind))))
	if kind == "" && allowEmpty {
		return "", nil
	}
	if kind == "" {
		kind = EntityKindTopic
	}
	switch kind {
	case EntityKindPerson, EntityKindProject, EntityKindPlace, EntityKindTopic:
		return kind, nil
	default:
		return "", fmt.Errorf("invalid entity kind %q", kind)
	}
}

func normalizeEntityRelation(relation string) (string, error) {
	relation = strings.ToLower(strings.TrimSpace(relation))
	if relation == "" {
		return "", fmt.Errorf("relation is required")
	}
	relation = strings.Join(strings.Fields(relation), "_")
	return relation, nil
}

func normalizeEntityFields(kind EntityKind, name string, aliases []string, notes string, lastSeenAt int64) (EntityKind, string, []string, string, int64, error) {
	normalizedKind, err := normalizeEntityKind(kind, false)
	if err != nil {
		return "", "", nil, "", 0, err
	}
	name = strings.TrimSpace(redact.String(name))
	if name == "" {
		return "", "", nil, "", 0, fmt.Errorf("name is required")
	}
	aliases = compactAliases(aliases)
	notes = strings.TrimSpace(redact.String(notes))
	if lastSeenAt < 0 {
		return "", "", nil, "", 0, fmt.Errorf("last_seen_at must be >= 0")
	}
	return normalizedKind, name, aliases, notes, lastSeenAt, nil
}

func newEntity(peerID string, kind EntityKind, name string, aliases []string, notes string, lastSeenAt int64) (*Entity, error) {
	normalizedKind, normalizedName, normalizedAliases, normalizedNotes, normalizedLastSeenAt, err := normalizeEntityFields(kind, name, aliases, notes, lastSeenAt)
	if err != nil {
		return nil, err
	}
	now := time.Now().Unix()
	if normalizedLastSeenAt == 0 {
		normalizedLastSeenAt = now
	}
	return &Entity{
		ID:         newID(),
		PeerID:     peerID,
		Kind:       normalizedKind,
		Name:       normalizedName,
		Aliases:    normalizedAliases,
		Notes:      normalizedNotes,
		CreatedAt:  now,
		UpdatedAt:  now,
		LastSeenAt: normalizedLastSeenAt,
	}, nil
}

func applyEntityPatch(entity Entity, patch EntityPatch) (Entity, error) {
	kind := entity.Kind
	if patch.Kind != nil {
		kind = *patch.Kind
	}
	name := entity.Name
	if patch.Name != nil {
		name = *patch.Name
	}
	aliases := append([]string(nil), entity.Aliases...)
	if patch.Aliases != nil {
		aliases = append([]string(nil), (*patch.Aliases)...)
	}
	notes := entity.Notes
	if patch.Notes != nil {
		notes = *patch.Notes
	}
	lastSeenAt := entity.LastSeenAt
	if patch.LastSeenAt != nil {
		lastSeenAt = *patch.LastSeenAt
	}
	normalizedKind, normalizedName, normalizedAliases, normalizedNotes, normalizedLastSeenAt, err := normalizeEntityFields(kind, name, aliases, notes, lastSeenAt)
	if err != nil {
		return Entity{}, err
	}
	entity.Kind = normalizedKind
	entity.Name = normalizedName
	entity.Aliases = normalizedAliases
	entity.Notes = normalizedNotes
	entity.LastSeenAt = normalizedLastSeenAt
	entity.UpdatedAt = time.Now().Unix()
	return entity, nil
}

func findEntityByIdentityTx(ctx context.Context, tx *sql.Tx, peerID string, kind EntityKind, name string, aliases []string) (*Entity, error) {
	rows, err := tx.QueryContext(ctx,
		`SELECT id, peer_id, kind, name, COALESCE(aliases,''), COALESCE(notes,''), created_at, updated_at, COALESCE(last_seen_at,0), (SELECT COUNT(*) FROM memory_entity_chunks WHERE entity_id = memory_entities.id)
		   FROM memory_entities
		  WHERE peer_id = ? AND kind = ?`,
		peerID, kind)
	if err != nil {
		return nil, fmt.Errorf("memory entity find: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		entity, err := scanEntity(rows)
		if err != nil {
			continue
		}
		if entityIdentityMatches(entity, name, aliases) {
			return &entity, nil
		}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("memory entity find: %w", err)
	}
	return nil, nil
}

func entityIdentityMatches(entity Entity, name string, aliases []string) bool {
	candidates := make(map[string]struct{}, len(aliases)+1)
	if normalized := strings.ToLower(strings.TrimSpace(name)); normalized != "" {
		candidates[normalized] = struct{}{}
	}
	for _, alias := range aliases {
		if normalized := strings.ToLower(strings.TrimSpace(alias)); normalized != "" {
			candidates[normalized] = struct{}{}
		}
	}
	if len(candidates) == 0 {
		return false
	}
	if _, ok := candidates[strings.ToLower(strings.TrimSpace(entity.Name))]; ok {
		return true
	}
	for _, alias := range entity.Aliases {
		if _, ok := candidates[strings.ToLower(strings.TrimSpace(alias))]; ok {
			return true
		}
	}
	return false
}

func mergeEntityNotes(existing, incoming string) string {
	existing = strings.TrimSpace(existing)
	incoming = strings.TrimSpace(incoming)
	switch {
	case incoming == "":
		return existing
	case existing == "":
		return incoming
	case strings.Contains(strings.ToLower(existing), strings.ToLower(incoming)):
		return existing
	default:
		return existing + "\n\n" + incoming
	}
}

func insertEntityTx(ctx context.Context, tx *sql.Tx, entity Entity) error {
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO memory_entities(id, peer_id, kind, name, aliases, notes, created_at, updated_at, last_seen_at) VALUES (?,?,?,?,?,?,?,?,?)`,
		entity.ID, entity.PeerID, entity.Kind, entity.Name, strings.Join(entity.Aliases, ","), entity.Notes, entity.CreatedAt, entity.UpdatedAt, entity.LastSeenAt); err != nil {
		return fmt.Errorf("memory entity create: %w", err)
	}
	if err := upsertEntityFTSTx(ctx, tx, entity); err != nil {
		return err
	}
	return nil
}

func updateEntityTx(ctx context.Context, tx *sql.Tx, entity Entity) error {
	if _, err := tx.ExecContext(ctx,
		`UPDATE memory_entities SET kind = ?, name = ?, aliases = ?, notes = ?, updated_at = ?, last_seen_at = ? WHERE id = ? AND peer_id = ?`,
		entity.Kind, entity.Name, strings.Join(entity.Aliases, ","), entity.Notes, entity.UpdatedAt, entity.LastSeenAt, entity.ID, entity.PeerID); err != nil {
		return fmt.Errorf("memory entity update: %w", err)
	}
	if err := upsertEntityFTSTx(ctx, tx, entity); err != nil {
		return err
	}
	return nil
}

func upsertEntityFTSTx(ctx context.Context, tx *sql.Tx, entity Entity) error {
	if _, err := tx.ExecContext(ctx, `DELETE FROM memory_entities_fts WHERE id = ?`, entity.ID); err != nil {
		return fmt.Errorf("memory entity fts delete: %w", err)
	}
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO memory_entities_fts(id, peer_id, name, aliases, notes) VALUES (?,?,?,?,?)`,
		entity.ID, entity.PeerID, entity.Name, strings.Join(entity.Aliases, " "), entity.Notes); err != nil {
		return fmt.Errorf("memory entity fts insert: %w", err)
	}
	return nil
}

func loadEntityTx(ctx context.Context, tx *sql.Tx, peerID, entityID string) (*Entity, error) {
	entity, err := scanEntity(tx.QueryRowContext(ctx,
		`SELECT id, peer_id, kind, name, COALESCE(aliases,''), COALESCE(notes,''), created_at, updated_at, COALESCE(last_seen_at,0), (SELECT COUNT(*) FROM memory_entity_chunks WHERE entity_id = memory_entities.id) FROM memory_entities WHERE id = ?`,
		entityID))
	if err == sql.ErrNoRows {
		return nil, fmt.Errorf("entity %s not found", entityID)
	}
	if err != nil {
		return nil, fmt.Errorf("memory entity get: %w", err)
	}
	if entity.PeerID != peerID {
		return nil, fmt.Errorf("entity %s does not belong to peer", entityID)
	}
	return &entity, nil
}

func loadEntityEdgeTx(ctx context.Context, tx *sql.Tx, peerID, sourceEntityID, targetEntityID, relation string) (*EntityRelationship, error) {
	edge, err := scanEntityRelationship(tx.QueryRowContext(ctx,
		`SELECT e.id, e.peer_id, e.source_entity_id, e.target_entity_id, e.relation, COALESCE(e.notes,''), e.created_at, e.updated_at, COALESCE(source.name,''), COALESCE(target.name,'')
		   FROM memory_entity_edges e
		   JOIN memory_entities source ON source.id = e.source_entity_id
		   JOIN memory_entities target ON target.id = e.target_entity_id
		  WHERE e.peer_id = ? AND e.source_entity_id = ? AND e.target_entity_id = ? AND e.relation = ?`,
		peerID, sourceEntityID, targetEntityID, relation))
	if err == sql.ErrNoRows {
		return nil, fmt.Errorf("entity relationship not found")
	}
	if err != nil {
		return nil, fmt.Errorf("memory entity relationship get: %w", err)
	}
	return &edge, nil
}

func (s *Store) listEntityChunks(ctx context.Context, peerID, entityID string) ([]Chunk, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT c.id, c.peer_id, c.content, c.created_at, COALESCE(c.tags,''), COALESCE(c.category,''), COALESCE(c.retention_class,?), COALESCE(c.exposure_policy,?), COALESCE(c.expires_at,0)
		   FROM memory_entity_chunks ec
		   JOIN chunks c ON c.id = ec.chunk_id
		  WHERE ec.entity_id = ? AND c.peer_id = ?
		  ORDER BY ec.created_at DESC`,
		RetentionClassWorking, ExposurePolicyAuto, entityID, peerID)
	if err != nil {
		return nil, fmt.Errorf("memory entity linked chunks: %w", err)
	}
	defer rows.Close()
	var chunks []Chunk
	for rows.Next() {
		chunk, err := scanChunk(rows)
		if err != nil {
			continue
		}
		chunks = append(chunks, chunk)
	}
	return chunks, rows.Err()
}

func (s *Store) listEntityEdges(ctx context.Context, peerID, entityID string, outgoing bool) ([]EntityRelationship, error) {
	column := "e.target_entity_id"
	filter := "e.source_entity_id"
	if !outgoing {
		column = "e.source_entity_id"
		filter = "e.target_entity_id"
	}
	query := fmt.Sprintf(`SELECT e.id, e.peer_id, e.source_entity_id, e.target_entity_id, e.relation, COALESCE(e.notes,''), e.created_at, e.updated_at, COALESCE(source.name,''), COALESCE(target.name,'')
	   FROM memory_entity_edges e
	   JOIN memory_entities source ON source.id = e.source_entity_id
	   JOIN memory_entities target ON target.id = e.target_entity_id
	  WHERE e.peer_id = ? AND %s = ?
	  ORDER BY e.updated_at DESC, %s ASC`, filter, column)
	rows, err := s.db.QueryContext(ctx, query, peerID, entityID)
	if err != nil {
		return nil, fmt.Errorf("memory entity relationships: %w", err)
	}
	defer rows.Close()
	var edges []EntityRelationship
	for rows.Next() {
		edge, err := scanEntityRelationship(rows)
		if err != nil {
			continue
		}
		edges = append(edges, edge)
	}
	return edges, rows.Err()
}

func maxInt64(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
}

func normalizeCandidateProvenance(provenance CandidateProvenance) (CandidateProvenance, error) {
	provenance.CaptureKind = strings.TrimSpace(provenance.CaptureKind)
	provenance.SourceSessionKey = strings.TrimSpace(provenance.SourceSessionKey)
	provenance.SourceExcerpt = truncateExcerpt(strings.TrimSpace(redact.String(provenance.SourceExcerpt)), 160)
	if provenance.CaptureKind == "" {
		provenance.CaptureKind = CandidateCaptureManual
	}
	switch provenance.CaptureKind {
	case CandidateCaptureManual, CandidateCaptureAutoTurnExtract:
		return provenance, nil
	default:
		return CandidateProvenance{}, fmt.Errorf("invalid capture_kind %q", provenance.CaptureKind)
	}
}

func truncateExcerpt(value string, limit int) string {
	if limit <= 0 || len(value) <= limit {
		return value
	}
	if limit <= 3 {
		return value[:limit]
	}
	return value[:limit-3] + "..."
}

func (s *Store) purgeExpired(ctx context.Context) error {
	now := time.Now().Unix()
	rows, err := s.db.QueryContext(ctx, `SELECT id FROM chunks WHERE expires_at > 0 AND expires_at <= ?`, now)
	if err != nil {
		return fmt.Errorf("memory purge expired lookup: %w", err)
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err == nil {
			ids = append(ids, id)
		}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("memory purge expired rows: %w", err)
	}
	if len(ids) == 0 {
		return nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("memory purge expired begin tx: %w", err)
	}
	defer tx.Rollback()
	for _, id := range ids {
		if _, err := tx.ExecContext(ctx, `DELETE FROM chunk_embeddings WHERE chunk_id = ?`, id); err != nil {
			return fmt.Errorf("memory purge expired embeddings: %w", err)
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM chunks_fts WHERE id = ?`, id); err != nil {
			return fmt.Errorf("memory purge expired fts: %w", err)
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM chunks WHERE id = ?`, id); err != nil {
			return fmt.Errorf("memory purge expired chunks: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("memory purge expired commit: %w", err)
	}
	if s.milvus != nil {
		go func(chunkIDs []string) {
			cctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			if err := s.milvus.Delete(cctx, chunkIDs); err != nil {
				slog.Warn("memory: milvus delete failed during expiry purge", "chunks", len(chunkIDs), "err", err)
			}
		}(append([]string(nil), ids...))
	}
	return nil
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
