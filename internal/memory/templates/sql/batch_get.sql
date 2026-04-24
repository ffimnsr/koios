SELECT id,
       peer_id,
       content,
       created_at,
       COALESCE(tags,''),
       COALESCE(category,''),
       COALESCE(retention_class,'working'),
       COALESCE(exposure_policy,'auto'),
       COALESCE(expires_at,0)
  FROM chunks WHERE peer_id = ? AND id IN (%s)
 ORDER BY created_at DESC
