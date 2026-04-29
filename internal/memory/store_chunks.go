package memory

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"time"

	"github.com/ffimnsr/koios/internal/redact"
)

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
	ChunkProvenance
}

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

// List returns all chunks stored for a peer, ordered by creation time descending.
func (s *Store) List(ctx context.Context, peerID string, limit int) ([]Chunk, error) {
	if limit <= 0 {
		limit = 100
	}
	if err := s.purgeExpired(ctx); err != nil {
		return nil, err
	}
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, peer_id, content, created_at, COALESCE(tags,''), COALESCE(category,''), COALESCE(retention_class,?), COALESCE(exposure_policy,?), COALESCE(expires_at,0), COALESCE(capture_kind,''), COALESCE(capture_reason,''), COALESCE(confidence,1.0), COALESCE(source_session_key,''), COALESCE(source_message_id,''), COALESCE(source_run_id,''), COALESCE(source_hook,''), COALESCE(source_candidate_id,''), COALESCE(source_excerpt,'') FROM chunks WHERE peer_id = ? ORDER BY created_at DESC LIMIT ?`,
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

// Recent returns the N most-recently created chunks for a peer in chronological order.
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
	chunk, err := scanChunk(s.db.QueryRowContext(ctx,
		`SELECT id, peer_id, content, created_at, COALESCE(tags,''), COALESCE(category,''), COALESCE(retention_class,?), COALESCE(exposure_policy,?), COALESCE(expires_at,0), COALESCE(capture_kind,''), COALESCE(capture_reason,''), COALESCE(confidence,1.0), COALESCE(source_session_key,''), COALESCE(source_message_id,''), COALESCE(source_run_id,''), COALESCE(source_hook,''), COALESCE(source_candidate_id,''), COALESCE(source_excerpt,'') FROM chunks WHERE id = ?`,
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

// DeleteChunk removes a single memory chunk by ID, enforcing peer ownership.
func (s *Store) DeleteChunk(ctx context.Context, peerID, chunkID string) error {
	if err := s.purgeExpired(ctx); err != nil {
		return err
	}
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
	if _, err := tx.ExecContext(ctx, `DELETE FROM memory_preferences WHERE peer_id = ?`, peerID); err != nil {
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

// SearchCompact returns compact search results.
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
		out[i] = SearchResultCompact{ID: r.ID, PeerID: r.PeerID, Preview: preview, Score: r.Score}
	}
	return out, nil
}

// Timeline returns chunks around a given anchor chunk.
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
	var anchorTime int64
	err := s.db.QueryRowContext(ctx, `SELECT created_at FROM chunks WHERE id = ? AND peer_id = ?`, anchorID, peerID).Scan(&anchorTime)
	if err != nil {
		return nil, fmt.Errorf("memory timeline: anchor lookup: %w", err)
	}
	var chunks []Chunk
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
	for i, j := 0, len(chunks)-1; i < j; i, j = i+1, j-1 {
		chunks[i], chunks[j] = chunks[j], chunks[i]
	}
	anchor, err := s.GetChunk(ctx, peerID, anchorID)
	if err == nil {
		chunks = append(chunks, *anchor)
	}
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

// Stats returns aggregate information about the peer's memory store.
func (s *Store) Stats(ctx context.Context, peerID string) (*MemoryStats, error) {
	if err := s.purgeExpired(ctx); err != nil {
		return nil, err
	}
	stats := &MemoryStats{
		ByCategory:        make(map[string]int),
		ByRetention:       make(map[RetentionClass]int),
		ByPreferenceKind:  make(map[PreferenceKind]int),
		ByPreferenceScope: make(map[PreferenceScope]int),
	}
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM chunks WHERE peer_id = ?`, peerID).Scan(&stats.TotalChunks)
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
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM memory_preferences WHERE peer_id = ?`, peerID).Scan(&stats.TotalPreferences); err != nil {
		return nil, fmt.Errorf("memory stats preferences: %w", err)
	}
	prefKindRows, err := s.db.QueryContext(ctx, `SELECT COALESCE(kind,'preference'), COUNT(*) FROM memory_preferences WHERE peer_id = ? GROUP BY COALESCE(kind,'preference')`, peerID)
	if err != nil {
		return nil, fmt.Errorf("memory stats preference kinds: %w", err)
	}
	defer prefKindRows.Close()
	for prefKindRows.Next() {
		var kind string
		var count int
		if err := prefKindRows.Scan(&kind, &count); err == nil {
			stats.ByPreferenceKind[PreferenceKind(kind)] = count
		}
	}
	prefScopeRows, err := s.db.QueryContext(ctx, `SELECT COALESCE(scope,'global'), COUNT(*) FROM memory_preferences WHERE peer_id = ? GROUP BY COALESCE(scope,'global')`, peerID)
	if err != nil {
		return nil, fmt.Errorf("memory stats preference scopes: %w", err)
	}
	defer prefScopeRows.Close()
	for prefScopeRows.Next() {
		var scope string
		var count int
		if err := prefScopeRows.Scan(&scope, &count); err == nil {
			stats.ByPreferenceScope[PreferenceScope(scope)] = count
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
	normalized, err := normalizeChunkOptions(ChunkOptions{Tags: tags, Category: category, RetentionClass: retentionClass, ExposurePolicy: exposurePolicy, ExpiresAt: expiresAt})
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `UPDATE chunks SET tags = ?, category = ?, retention_class = ?, exposure_policy = ?, expires_at = ? WHERE id = ?`, strings.Join(normalized.Tags, ","), normalized.Category, normalized.RetentionClass, normalized.ExposurePolicy, normalized.ExpiresAt, chunkID)
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
		out[i] = SearchResult{ID: r.ID, Content: r.Document, RetentionClass: RetentionClassWorking, ExposurePolicy: ExposurePolicyAuto, Score: float64(r.Score)}
		if pid, ok := r.Metadata["peer_id"].(string); ok {
			out[i].PeerID = pid
		}
		chunk, err := s.GetChunk(ctx, peerID, r.ID)
		if err == nil {
			out[i].PeerID = chunk.PeerID
			out[i].Tags = append([]string(nil), chunk.Tags...)
			out[i].Category = chunk.Category
			out[i].RetentionClass = chunk.RetentionClass
			out[i].ExposurePolicy = chunk.ExposurePolicy
			out[i].ExpiresAt = chunk.ExpiresAt
			out[i].ChunkProvenance = chunk.ChunkProvenance
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
	meta := map[string]any{"peer_id": peerID, "category": category}
	if len(tags) > 0 {
		meta["tags"] = strings.Join(tags, ",")
	}
	if err := s.milvus.Upsert(ctx, []string{id}, []string{content}, [][]float32{emb}, []map[string]any{meta}); err != nil {
		slog.Warn("memory: milvus upsert failed", "chunk", id, "err", err)
	}
}

func mergeResults(bm25, milvusRes []SearchResult, topK int) []SearchResult {
	seen := make(map[string]SearchResult, len(bm25)+len(milvusRes))
	for i, r := range bm25 {
		r.Score = 1.0 - float64(i)*0.05
		seen[r.ID] = r
	}
	for i, r := range milvusRes {
		milvusScore := 1.0 - float64(i)*0.05
		if existing, ok := seen[r.ID]; ok {
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
	sort.Slice(merged, func(i, j int) bool { return merged[i].Score > merged[j].Score })
	if len(merged) > topK {
		merged = merged[:topK]
	}
	return merged
}

func insertChunkTx(ctx context.Context, tx *sql.Tx, peerID, content string, opts ChunkOptions, createdAt int64) (*Chunk, error) {
	chunk := &Chunk{ID: newID(), PeerID: peerID, Content: content, CreatedAt: createdAt, Tags: opts.Tags, Category: opts.Category, RetentionClass: opts.RetentionClass, ExposurePolicy: opts.ExposurePolicy, ExpiresAt: opts.ExpiresAt, ChunkProvenance: opts.Provenance}
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO chunks(id, peer_id, content, created_at, tags, category, retention_class, exposure_policy, expires_at, capture_kind, capture_reason, confidence, source_session_key, source_message_id, source_run_id, source_hook, source_candidate_id, source_excerpt) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		chunk.ID, chunk.PeerID, chunk.Content, chunk.CreatedAt, strings.Join(chunk.Tags, ","), chunk.Category, chunk.RetentionClass, chunk.ExposurePolicy, chunk.ExpiresAt, chunk.CaptureKind, chunk.CaptureReason, chunk.Confidence, chunk.SourceSessionKey, chunk.SourceMessageID, chunk.SourceRunID, chunk.SourceHook, chunk.SourceCandidateID, chunk.SourceExcerpt); err != nil {
		return nil, fmt.Errorf("memory insert: chunks: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO chunks_fts(id, peer_id, content) VALUES (?,?,?)`, chunk.ID, chunk.PeerID, chunk.Content); err != nil {
		return nil, fmt.Errorf("memory insert: fts: %w", err)
	}
	return chunk, nil
}

func updateChunkTx(ctx context.Context, tx *sql.Tx, chunk Chunk) error {
	if _, err := tx.ExecContext(ctx,
		`UPDATE chunks SET content = ?, tags = ?, category = ?, retention_class = ?, exposure_policy = ?, expires_at = ?, capture_kind = ?, capture_reason = ?, confidence = ?, source_session_key = ?, source_message_id = ?, source_run_id = ?, source_hook = ?, source_candidate_id = ?, source_excerpt = ? WHERE id = ? AND peer_id = ?`,
		chunk.Content, strings.Join(chunk.Tags, ","), chunk.Category, chunk.RetentionClass, chunk.ExposurePolicy, chunk.ExpiresAt, chunk.CaptureKind, chunk.CaptureReason, chunk.Confidence, chunk.SourceSessionKey, chunk.SourceMessageID, chunk.SourceRunID, chunk.SourceHook, chunk.SourceCandidateID, chunk.SourceExcerpt, chunk.ID, chunk.PeerID); err != nil {
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
		`SELECT id, peer_id, content, created_at, COALESCE(tags,''), COALESCE(category,''), COALESCE(retention_class,?), COALESCE(exposure_policy,?), COALESCE(expires_at,0), COALESCE(capture_kind,''), COALESCE(capture_reason,''), COALESCE(confidence,1.0), COALESCE(source_session_key,''), COALESCE(source_message_id,''), COALESCE(source_run_id,''), COALESCE(source_hook,''), COALESCE(source_candidate_id,''), COALESCE(source_excerpt,'') FROM chunks WHERE id = ?`,
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
		if err := rows.Scan(&r.ID, &r.PeerID, &r.Content, &tagStr, &r.Category, &r.RetentionClass, &r.ExposurePolicy, &r.ExpiresAt, &r.CaptureKind, &r.CaptureReason, &r.Confidence, &r.SourceSessionKey, &r.SourceMessageID, &r.SourceRunID, &r.SourceHook, &r.SourceCandidateID, &r.SourceExcerpt, &rank); err != nil {
			continue
		}
		r.Tags = parseTags(tagStr)
		if r.Confidence == 0 {
			r.Confidence = 1
		}
		r.Score = -rank
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
	if _, err := s.db.ExecContext(ctx, `INSERT OR REPLACE INTO chunk_embeddings(chunk_id, embedding) VALUES (?,?)`, chunkID, encodeEmbedding(emb)); err != nil {
		slog.Warn("memory: store embedding", "chunk", chunkID, "err", err)
	}
}

func (s *Store) loadEmbedding(chunkID string) ([]float32, error) {
	var blob []byte
	err := s.db.QueryRowContext(context.Background(), `SELECT embedding FROM chunk_embeddings WHERE chunk_id = ?`, chunkID).Scan(&blob)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return decodeEmbedding(blob), nil
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
