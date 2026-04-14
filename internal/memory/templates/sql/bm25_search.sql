SELECT id, peer_id, content, rank
  FROM chunks_fts
 WHERE chunks_fts MATCH ?
   AND peer_id = ?
 ORDER BY rank
 LIMIT ?
