SELECT COALESCE(NULLIF(category,''),'uncategorized'), COUNT(*)
  FROM chunks WHERE peer_id = ? GROUP BY category
