package channels

import "strings"

const (
	TextChunkModeParagraph = "paragraph"
	TextChunkModeWord      = "word"
	TextChunkModeHard      = "hard"
)

type TextChunkPolicy struct {
	Limit int
	Mode  string
}

func NormalizeTextChunkMode(mode string) string {
	switch strings.ToLower(strings.TrimSpace(mode)) {
	case "", TextChunkModeParagraph:
		return TextChunkModeParagraph
	case TextChunkModeWord, TextChunkModeHard:
		return strings.ToLower(strings.TrimSpace(mode))
	default:
		return strings.ToLower(strings.TrimSpace(mode))
	}
}

func ChunkText(text string, limit int) []string {
	return ChunkTextWithPolicy(text, TextChunkPolicy{Limit: limit, Mode: TextChunkModeParagraph})
}

func ChunkTextWithPolicy(text string, policy TextChunkPolicy) []string {
	trimmed := strings.TrimSpace(text)
	if trimmed == "" {
		return nil
	}
	if policy.Limit <= 0 {
		return []string{trimmed}
	}
	policy.Mode = NormalizeTextChunkMode(policy.Mode)

	runes := []rune(trimmed)
	if len(runes) <= policy.Limit {
		return []string{trimmed}
	}

	chunks := make([]string, 0, (len(runes)/policy.Limit)+1)
	for start := 0; start < len(runes); {
		remaining := len(runes) - start
		if remaining <= policy.Limit {
			chunk := strings.TrimSpace(string(runes[start:]))
			if chunk != "" {
				chunks = append(chunks, chunk)
			}
			break
		}

		end := start + policy.Limit
		split := findChunkBoundary(runes, start, end, policy.Mode)
		if split <= start {
			split = end
		}

		chunk := strings.TrimSpace(string(runes[start:split]))
		if chunk != "" {
			chunks = append(chunks, chunk)
		}
		start = split
		for start < len(runes) && isChunkWhitespace(runes[start]) {
			start++
		}
	}
	return chunks
}

func findChunkBoundary(runes []rune, start, end int, mode string) int {
	if mode == TextChunkModeHard {
		return end
	}
	if mode == TextChunkModeParagraph {
		if split := findParagraphBoundary(runes, start, end); split > start {
			return split
		}
		if split := findNewlineBoundary(runes, start, end); split > start {
			return split
		}
	}
	if split := findWhitespaceBoundary(runes, start, end); split > start {
		return split
	}
	return end
}

func findParagraphBoundary(runes []rune, start, end int) int {
	if end+1 < len(runes) && runes[end] == '\n' && runes[end+1] == '\n' {
		return end
	}
	for i := end - 1; i > start; i-- {
		if runes[i] == '\n' && i+1 < len(runes) && runes[i+1] == '\n' {
			return i
		}
	}
	return 0
}

func findNewlineBoundary(runes []rune, start, end int) int {
	if end < len(runes) && runes[end] == '\n' {
		return end
	}
	for i := end - 1; i > start; i-- {
		if runes[i] == '\n' {
			return i
		}
	}
	return 0
}

func findWhitespaceBoundary(runes []rune, start, end int) int {
	if end < len(runes) && isChunkWhitespace(runes[end]) {
		return end
	}
	for i := end - 1; i > start; i-- {
		if isChunkWhitespace(runes[i]) {
			return i
		}
	}
	return 0
}

func isChunkWhitespace(r rune) bool {
	return r == ' ' || r == '\n' || r == '\t' || r == '\r'
}
