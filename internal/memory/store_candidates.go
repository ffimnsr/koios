package memory

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	"github.com/ffimnsr/koios/internal/redact"
)

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
	var rows *sql.Rows
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
	storeOpts := opts
	storeOpts.Provenance = candidateChunkProvenance(*candidate, reason)
	chunk, err := insertChunkTx(ctx, tx, peerID, content, storeOpts, time.Now().Unix())
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
	opts := ChunkOptions{Tags: append([]string(nil), candidate.Tags...), Category: candidate.Category, RetentionClass: candidate.RetentionClass, ExposurePolicy: candidate.ExposurePolicy, ExpiresAt: candidate.ExpiresAt}
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

func candidateChunkProvenance(candidate Candidate, reviewReason string) ChunkProvenance {
	provenance, err := normalizeChunkProvenance(ChunkProvenance{
		CaptureKind:       candidate.CaptureKind,
		CaptureReason:     strings.TrimSpace(reviewReason),
		Confidence:        1,
		SourceSessionKey:  candidate.SourceSessionKey,
		SourceCandidateID: candidate.ID,
		SourceExcerpt:     candidate.SourceExcerpt,
	})
	if err != nil {
		return ChunkProvenance{CaptureKind: CandidateCaptureManual, Confidence: 1}
	}
	return provenance
}
