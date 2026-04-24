SELECT memory_entities.id,
       memory_entities.peer_id,
       memory_entities.kind,
       memory_entities.name,
       COALESCE(memory_entities.aliases,''),
       COALESCE(memory_entities.notes,''),
       memory_entities.created_at,
       memory_entities.updated_at,
       COALESCE(memory_entities.last_seen_at,0),
       (SELECT COUNT(*) FROM memory_entity_chunks WHERE entity_id = memory_entities.id)
  FROM memory_entities_fts
  JOIN memory_entities ON memory_entities.id = memory_entities_fts.id
 WHERE memory_entities_fts MATCH ?
   AND memory_entities.peer_id = ?
 ORDER BY rank
 LIMIT ?
