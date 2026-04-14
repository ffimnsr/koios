package memory

import _ "embed"

//go:embed templates/sql/batch_get.sql
var batchGetQueryTemplate string

//go:embed templates/sql/stats_by_category.sql
var statsByCategoryQuery string

//go:embed templates/sql/bm25_search.sql
var bm25SearchQuery string

//go:embed templates/sql/recent_chunks.sql
var recentChunksQuery string

//go:embed templates/sql/timeline_before.sql
var timelineBeforeQuery string

//go:embed templates/sql/timeline_after.sql
var timelineAfterQuery string

//go:embed templates/sql/schema.sql
var schemaSQL string
