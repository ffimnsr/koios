SELECT chunks_fts.id,
       chunks.peer_id,
       chunks.content,
       COALESCE(chunks.tags,''),
       COALESCE(chunks.category,''),
       COALESCE(chunks.retention_class,'working'),
       COALESCE(chunks.exposure_policy,'auto'),
       COALESCE(chunks.expires_at,0),
        COALESCE(chunks.capture_kind,''),
        COALESCE(chunks.capture_reason,''),
        COALESCE(chunks.confidence,1.0),
        COALESCE(chunks.source_session_key,''),
        COALESCE(chunks.source_message_id,''),
        COALESCE(chunks.source_run_id,''),
        COALESCE(chunks.source_hook,''),
        COALESCE(chunks.source_candidate_id,''),
        COALESCE(chunks.source_excerpt,''),
       rank
  FROM chunks_fts
  JOIN chunks ON chunks.id = chunks_fts.id
 WHERE chunks_fts MATCH ?
   AND chunks.peer_id = ?
   AND COALESCE(chunks.exposure_policy,'auto') = 'auto'
   AND (COALESCE(chunks.expires_at,0) = 0 OR chunks.expires_at > strftime('%s','now'))
 ORDER BY rank
 LIMIT ?
