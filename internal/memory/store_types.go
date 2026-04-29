package memory

// SearchResult is a single memory chunk returned by a search query.
type SearchResult struct {
	ID             string
	PeerID         string
	Content        string
	Tags           []string
	Category       string
	RetentionClass RetentionClass
	ExposurePolicy ExposurePolicy
	ExpiresAt      int64
	ChunkProvenance
	Score float64 // BM25 rank (negative) or cosine similarity [0,1]
}

type RetentionClass string

const (
	RetentionClassWorking RetentionClass = "working"
	RetentionClassPinned  RetentionClass = "pinned"
	RetentionClassArchive RetentionClass = "archive"
)

type ExposurePolicy string

const (
	ExposurePolicyAuto       ExposurePolicy = "auto"
	ExposurePolicySearchOnly ExposurePolicy = "search_only"
)

type CandidateStatus string

const (
	CandidateStatusPending  CandidateStatus = "pending"
	CandidateStatusApproved CandidateStatus = "approved"
	CandidateStatusMerged   CandidateStatus = "merged"
	CandidateStatusRejected CandidateStatus = "rejected"
	CandidateStatusAll      CandidateStatus = "all"
)

const (
	CandidateCaptureManual           = "manual"
	CandidateCaptureAutoTurnExtract  = "auto_turn_extract"
	ChunkCaptureConversationArchive  = "conversation_archive"
	ChunkCaptureCompactionCheckpoint = "compaction_checkpoint"
)

type ChunkOptions struct {
	Tags           []string
	Category       string
	RetentionClass RetentionClass
	ExposurePolicy ExposurePolicy
	ExpiresAt      int64
	Provenance     ChunkProvenance
}

type ChunkProvenance struct {
	CaptureKind       string  `json:"capture_kind,omitempty"`
	CaptureReason     string  `json:"capture_reason,omitempty"`
	Confidence        float64 `json:"confidence,omitempty"`
	SourceSessionKey  string  `json:"source_session_key,omitempty"`
	SourceMessageID   string  `json:"source_message_id,omitempty"`
	SourceRunID       string  `json:"source_run_id,omitempty"`
	SourceHook        string  `json:"source_hook,omitempty"`
	SourceCandidateID string  `json:"source_candidate_id,omitempty"`
	SourceExcerpt     string  `json:"source_excerpt,omitempty"`
}

type CandidateProvenance struct {
	CaptureKind      string
	SourceSessionKey string
	SourceExcerpt    string
}

type Candidate struct {
	ID               string          `json:"id"`
	PeerID           string          `json:"peer_id"`
	Content          string          `json:"content"`
	CreatedAt        int64           `json:"created_at"`
	Tags             []string        `json:"tags,omitempty"`
	Category         string          `json:"category,omitempty"`
	RetentionClass   RetentionClass  `json:"retention_class,omitempty"`
	ExposurePolicy   ExposurePolicy  `json:"exposure_policy,omitempty"`
	ExpiresAt        int64           `json:"expires_at,omitempty"`
	Status           CandidateStatus `json:"status"`
	ReviewReason     string          `json:"review_reason,omitempty"`
	ReviewedAt       int64           `json:"reviewed_at,omitempty"`
	ResultChunkID    string          `json:"result_chunk_id,omitempty"`
	MergeIntoID      string          `json:"merge_into_id,omitempty"`
	CaptureKind      string          `json:"capture_kind,omitempty"`
	SourceSessionKey string          `json:"source_session_key,omitempty"`
	SourceExcerpt    string          `json:"source_excerpt,omitempty"`
}

type CandidatePatch struct {
	Content        *string
	Tags           *[]string
	Category       *string
	RetentionClass *RetentionClass
	ExposurePolicy *ExposurePolicy
	ExpiresAt      *int64
}

type EntityKind string

const (
	EntityKindPerson  EntityKind = "person"
	EntityKindProject EntityKind = "project"
	EntityKindPlace   EntityKind = "place"
	EntityKindTopic   EntityKind = "topic"
)

type Entity struct {
	ID               string     `json:"id"`
	PeerID           string     `json:"peer_id"`
	Kind             EntityKind `json:"kind"`
	Name             string     `json:"name"`
	Aliases          []string   `json:"aliases,omitempty"`
	Notes            string     `json:"notes,omitempty"`
	CreatedAt        int64      `json:"created_at"`
	UpdatedAt        int64      `json:"updated_at"`
	LastSeenAt       int64      `json:"last_seen_at,omitempty"`
	LinkedChunkCount int        `json:"linked_chunk_count,omitempty"`
}

type EntityPatch struct {
	Kind       *EntityKind
	Name       *string
	Aliases    *[]string
	Notes      *string
	LastSeenAt *int64
}

type EntityRelationship struct {
	ID             string `json:"id"`
	PeerID         string `json:"peer_id"`
	SourceEntityID string `json:"source_entity_id"`
	TargetEntityID string `json:"target_entity_id"`
	Relation       string `json:"relation"`
	Notes          string `json:"notes,omitempty"`
	CreatedAt      int64  `json:"created_at"`
	UpdatedAt      int64  `json:"updated_at"`
	SourceName     string `json:"source_name,omitempty"`
	TargetName     string `json:"target_name,omitempty"`
}

type EntityGraph struct {
	Entity       Entity               `json:"entity"`
	LinkedChunks []Chunk              `json:"linked_chunks,omitempty"`
	Outgoing     []EntityRelationship `json:"outgoing_relationships,omitempty"`
	Incoming     []EntityRelationship `json:"incoming_relationships,omitempty"`
}

type PreferenceKind string

const (
	PreferenceKindPreference PreferenceKind = "preference"
	PreferenceKindDecision   PreferenceKind = "decision"
)

type PreferenceScope string

const (
	PreferenceScopeGlobal    PreferenceScope = "global"
	PreferenceScopeWorkspace PreferenceScope = "workspace"
	PreferenceScopeProfile   PreferenceScope = "profile"
	PreferenceScopeProject   PreferenceScope = "project"
	PreferenceScopeTopic     PreferenceScope = "topic"
)

type PreferenceRecord struct {
	ID               string          `json:"id"`
	PeerID           string          `json:"peer_id"`
	Kind             PreferenceKind  `json:"kind"`
	Name             string          `json:"name"`
	Value            string          `json:"value"`
	Category         string          `json:"category,omitempty"`
	Scope            PreferenceScope `json:"scope"`
	ScopeRef         string          `json:"scope_ref,omitempty"`
	Confidence       float64         `json:"confidence"`
	LastConfirmedAt  int64           `json:"last_confirmed_at,omitempty"`
	CreatedAt        int64           `json:"created_at"`
	UpdatedAt        int64           `json:"updated_at"`
	SourceSessionKey string          `json:"source_session_key,omitempty"`
	SourceExcerpt    string          `json:"source_excerpt,omitempty"`
}

type PreferencePatch struct {
	Kind             *PreferenceKind
	Name             *string
	Value            *string
	Category         *string
	Scope            *PreferenceScope
	ScopeRef         *string
	Confidence       *float64
	LastConfirmedAt  *int64
	SourceSessionKey *string
	SourceExcerpt    *string
}

type PreferenceFilter struct {
	Kind  PreferenceKind
	Scope PreferenceScope
	Limit int
}

// SearchResultCompact is a lightweight search result for progressive disclosure.
type SearchResultCompact struct {
	ID      string  `json:"id"`
	PeerID  string  `json:"peer_id"`
	Preview string  `json:"preview"`
	Score   float64 `json:"score"`
}

// MemoryStats holds aggregate information about the memory store.
type MemoryStats struct {
	TotalChunks       int                     `json:"total_chunks"`
	ByCategory        map[string]int          `json:"by_category"`
	ByRetention       map[RetentionClass]int  `json:"by_retention"`
	TotalPreferences  int                     `json:"total_preferences"`
	ByPreferenceKind  map[PreferenceKind]int  `json:"by_preference_kind"`
	ByPreferenceScope map[PreferenceScope]int `json:"by_preference_scope"`
	MilvusActive      bool                    `json:"milvus_active"`
	MilvusCount       int                     `json:"milvus_count"`
}

type rowScanner interface {
	Scan(dest ...any) error
}
