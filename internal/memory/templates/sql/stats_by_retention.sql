SELECT COALESCE(NULLIF(retention_class,''),'working'), COUNT(*)
  FROM chunks WHERE peer_id = ? GROUP BY retention_class
