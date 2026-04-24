SELECT chunks_fts.id,
       chunks.peer_id,
       chunks.content,
       COALESCE(chunks.tags,''),
       COALESCE(chunks.category,''),
       COALESCE(chunks.retention_class,'working'),
       COALESCE(chunks.exposure_policy,'auto'),
       COALESCE(chunks.expires_at,0),
       rank
  FROM chunks_fts
  JOIN chunks ON chunks.id = chunks_fts.id
 WHERE chunks_fts MATCH ?
   AND chunks.peer_id = ?
   AND COALESCE(chunks.exposure_policy,'auto') = 'auto'
   AND (COALESCE(chunks.expires_at,0) = 0 OR chunks.expires_at > strftime('%s','now'))
 ORDER BY rank
 LIMIT ?
