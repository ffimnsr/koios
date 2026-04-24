SELECT id,
       peer_id,
       content,
       created_at,
       COALESCE(tags,''),
       COALESCE(category,''),
       COALESCE(retention_class,'working'),
       COALESCE(exposure_policy,'auto'),
       COALESCE(expires_at,0)
  FROM chunks
 WHERE peer_id = ?
   AND COALESCE(exposure_policy,'auto') = 'auto'
   AND (COALESCE(expires_at,0) = 0 OR expires_at > strftime('%s','now'))
 ORDER BY created_at DESC LIMIT ?
