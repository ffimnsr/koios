SELECT id, peer_id, content, created_at, COALESCE(tags,''), COALESCE(category,'')
  FROM chunks WHERE peer_id = ?
 ORDER BY created_at DESC LIMIT ?
