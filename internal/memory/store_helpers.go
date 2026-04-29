package memory

import (
	"crypto/rand"
	"database/sql"
	"encoding/binary"
	"fmt"
	"math"
	"strings"
	"time"
	"unicode"

	"github.com/ffimnsr/koios/internal/redact"
)

func migrate(db *sql.DB) error {
	_, err := db.Exec(schemaSQL)
	if err != nil {
		return err
	}
	_, _ = db.Exec(`ALTER TABLE chunks ADD COLUMN tags TEXT NOT NULL DEFAULT ''`)
	_, _ = db.Exec(`ALTER TABLE chunks ADD COLUMN category TEXT NOT NULL DEFAULT ''`)
	_, _ = db.Exec(`ALTER TABLE chunks ADD COLUMN retention_class TEXT NOT NULL DEFAULT 'working'`)
	_, _ = db.Exec(`ALTER TABLE chunks ADD COLUMN exposure_policy TEXT NOT NULL DEFAULT 'auto'`)
	_, _ = db.Exec(`ALTER TABLE chunks ADD COLUMN expires_at INTEGER NOT NULL DEFAULT 0`)
	_, _ = db.Exec(`ALTER TABLE chunks ADD COLUMN capture_kind TEXT NOT NULL DEFAULT 'manual'`)
	_, _ = db.Exec(`ALTER TABLE chunks ADD COLUMN capture_reason TEXT NOT NULL DEFAULT ''`)
	_, _ = db.Exec(`ALTER TABLE chunks ADD COLUMN confidence REAL NOT NULL DEFAULT 1.0`)
	_, _ = db.Exec(`ALTER TABLE chunks ADD COLUMN source_session_key TEXT NOT NULL DEFAULT ''`)
	_, _ = db.Exec(`ALTER TABLE chunks ADD COLUMN source_message_id TEXT NOT NULL DEFAULT ''`)
	_, _ = db.Exec(`ALTER TABLE chunks ADD COLUMN source_run_id TEXT NOT NULL DEFAULT ''`)
	_, _ = db.Exec(`ALTER TABLE chunks ADD COLUMN source_hook TEXT NOT NULL DEFAULT ''`)
	_, _ = db.Exec(`ALTER TABLE chunks ADD COLUMN source_candidate_id TEXT NOT NULL DEFAULT ''`)
	_, _ = db.Exec(`ALTER TABLE chunks ADD COLUMN source_excerpt TEXT NOT NULL DEFAULT ''`)
	_, _ = db.Exec(`ALTER TABLE memory_candidates ADD COLUMN capture_kind TEXT NOT NULL DEFAULT 'manual'`)
	_, _ = db.Exec(`ALTER TABLE memory_candidates ADD COLUMN source_session_key TEXT NOT NULL DEFAULT ''`)
	_, _ = db.Exec(`ALTER TABLE memory_candidates ADD COLUMN source_excerpt TEXT NOT NULL DEFAULT ''`)
	_, _ = db.Exec(`ALTER TABLE memory_preferences ADD COLUMN category TEXT NOT NULL DEFAULT ''`)
	_, _ = db.Exec(`ALTER TABLE memory_preferences ADD COLUMN scope_ref TEXT NOT NULL DEFAULT ''`)
	_, _ = db.Exec(`ALTER TABLE memory_preferences ADD COLUMN source_session_key TEXT NOT NULL DEFAULT ''`)
	_, _ = db.Exec(`ALTER TABLE memory_preferences ADD COLUMN source_excerpt TEXT NOT NULL DEFAULT ''`)
	_, _ = db.Exec(`CREATE INDEX IF NOT EXISTS idx_memory_candidates_peer_status_created_at ON memory_candidates(peer_id, status, created_at DESC)`)
	_, _ = db.Exec(`CREATE INDEX IF NOT EXISTS idx_memory_preferences_peer_kind_scope ON memory_preferences(peer_id, kind, scope, updated_at DESC)`)
	_, _ = db.Exec(`CREATE INDEX IF NOT EXISTS idx_memory_preferences_peer_confirmed ON memory_preferences(peer_id, last_confirmed_at DESC, confidence DESC)`)
	_, _ = db.Exec(`CREATE INDEX IF NOT EXISTS idx_memory_entities_peer_kind_name ON memory_entities(peer_id, kind, name)`)
	_, _ = db.Exec(`CREATE UNIQUE INDEX IF NOT EXISTS idx_memory_entity_edges_unique ON memory_entity_edges(peer_id, source_entity_id, target_entity_id, relation)`)
	_, _ = db.Exec(`CREATE INDEX IF NOT EXISTS idx_memory_entity_edges_source ON memory_entity_edges(peer_id, source_entity_id, updated_at DESC)`)
	_, _ = db.Exec(`CREATE INDEX IF NOT EXISTS idx_memory_entity_edges_target ON memory_entity_edges(peer_id, target_entity_id, updated_at DESC)`)
	return nil
}

func newID() string {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		return fmt.Sprintf("%x", time.Now().UnixNano())
	}
	return fmt.Sprintf("%x", b)
}

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
	var err error
	normalized.Provenance, err = normalizeChunkProvenance(normalized.Provenance)
	if err != nil {
		return ChunkOptions{}, err
	}
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

func normalizeChunkProvenance(provenance ChunkProvenance) (ChunkProvenance, error) {
	provenance.CaptureKind = strings.TrimSpace(provenance.CaptureKind)
	provenance.CaptureReason = truncateExcerpt(strings.TrimSpace(redact.String(provenance.CaptureReason)), 160)
	provenance.SourceSessionKey = strings.TrimSpace(provenance.SourceSessionKey)
	provenance.SourceMessageID = strings.TrimSpace(provenance.SourceMessageID)
	provenance.SourceRunID = strings.TrimSpace(provenance.SourceRunID)
	provenance.SourceHook = strings.TrimSpace(provenance.SourceHook)
	provenance.SourceCandidateID = strings.TrimSpace(provenance.SourceCandidateID)
	provenance.SourceExcerpt = truncateExcerpt(strings.TrimSpace(redact.String(provenance.SourceExcerpt)), 160)
	if provenance.CaptureKind == "" {
		provenance.CaptureKind = CandidateCaptureManual
	}
	if provenance.Confidence == 0 {
		provenance.Confidence = 1
	}
	if provenance.Confidence < 0 || provenance.Confidence > 1 {
		return ChunkProvenance{}, fmt.Errorf("confidence must be between 0 and 1")
	}
	return provenance, nil
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

func scanChunk(scanner rowScanner) (Chunk, error) {
	var c Chunk
	var tagStr string
	err := scanner.Scan(&c.ID, &c.PeerID, &c.Content, &c.CreatedAt, &tagStr, &c.Category, &c.RetentionClass, &c.ExposurePolicy, &c.ExpiresAt, &c.CaptureKind, &c.CaptureReason, &c.Confidence, &c.SourceSessionKey, &c.SourceMessageID, &c.SourceRunID, &c.SourceHook, &c.SourceCandidateID, &c.SourceExcerpt)
	if err != nil {
		return Chunk{}, err
	}
	c.Tags = parseTags(tagStr)
	if c.Confidence == 0 {
		c.Confidence = 1
	}
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

func scanPreference(scanner rowScanner) (PreferenceRecord, error) {
	var record PreferenceRecord
	err := scanner.Scan(&record.ID, &record.PeerID, &record.Kind, &record.Name, &record.Value, &record.Category, &record.Scope, &record.ScopeRef, &record.Confidence, &record.LastConfirmedAt, &record.CreatedAt, &record.UpdatedAt, &record.SourceSessionKey, &record.SourceExcerpt)
	if err != nil {
		return PreferenceRecord{}, err
	}
	return record, nil
}

func normalizePreferenceKind(kind PreferenceKind, allowEmpty bool) (PreferenceKind, error) {
	kind = PreferenceKind(strings.ToLower(strings.TrimSpace(string(kind))))
	if kind == "" && allowEmpty {
		return "", nil
	}
	if kind == "" {
		kind = PreferenceKindPreference
	}
	switch kind {
	case PreferenceKindPreference, PreferenceKindDecision:
		return kind, nil
	default:
		return "", fmt.Errorf("invalid preference kind %q", kind)
	}
}

func normalizePreferenceScope(scope PreferenceScope, allowEmpty bool) (PreferenceScope, error) {
	scope = PreferenceScope(strings.ToLower(strings.TrimSpace(string(scope))))
	if scope == "" && allowEmpty {
		return "", nil
	}
	if scope == "" {
		scope = PreferenceScopeGlobal
	}
	switch scope {
	case PreferenceScopeGlobal, PreferenceScopeWorkspace, PreferenceScopeProfile, PreferenceScopeProject, PreferenceScopeTopic:
		return scope, nil
	default:
		return "", fmt.Errorf("invalid preference scope %q", scope)
	}
}

func normalizePreferenceConfidence(confidence float64, defaultValue float64) (float64, error) {
	if confidence == 0 {
		confidence = defaultValue
	}
	if confidence < 0 || confidence > 1 {
		return 0, fmt.Errorf("confidence must be between 0 and 1")
	}
	return confidence, nil
}

func maxInt64(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
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
