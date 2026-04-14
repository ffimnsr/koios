SELECT id, peer_id, content, created_at, COALESCE(tags,''), COALESCE(category,'')
  FROM chunks WHERE peer_id = ? AND id IN (%s)
 ORDER BY created_at DESC
