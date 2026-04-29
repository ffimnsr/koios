package memory

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	"github.com/ffimnsr/koios/internal/redact"
)

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
		entity := Entity{ID: newID(), PeerID: peerID, Kind: normalizedKind, Name: normalizedName, Aliases: normalizedAliases, Notes: normalizedNotes, CreatedAt: normalizedLastSeenAt, UpdatedAt: normalizedLastSeenAt, LastSeenAt: normalizedLastSeenAt}
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
	return &EntityGraph{Entity: *entity, LinkedChunks: linkedChunks, Outgoing: outgoing, Incoming: incoming}, nil
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
	return &Entity{ID: newID(), PeerID: peerID, Kind: normalizedKind, Name: normalizedName, Aliases: normalizedAliases, Notes: normalizedNotes, CreatedAt: now, UpdatedAt: now, LastSeenAt: normalizedLastSeenAt}, nil
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
		`SELECT c.id, c.peer_id, c.content, c.created_at, COALESCE(c.tags,''), COALESCE(c.category,''), COALESCE(c.retention_class,?), COALESCE(c.exposure_policy,?), COALESCE(c.expires_at,0), COALESCE(c.capture_kind,''), COALESCE(c.capture_reason,''), COALESCE(c.confidence,1.0), COALESCE(c.source_session_key,''), COALESCE(c.source_message_id,''), COALESCE(c.source_run_id,''), COALESCE(c.source_hook,''), COALESCE(c.source_candidate_id,''), COALESCE(c.source_excerpt,'')
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
