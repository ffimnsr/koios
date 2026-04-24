package agent

import _ "embed"

//go:embed templates/memory_candidate_extractor_prompt.txt
var memoryCandidateExtractorPrompt string

//go:embed templates/entity_extractor_prompt.txt
var entityExtractorPrompt string
