package channels

import (
	"reflect"
	"testing"
)

func TestChunkTextReturnsSingleChunkWhenWithinLimit(t *testing.T) {
	got := ChunkText("hello world", 50)
	want := []string{"hello world"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ChunkText() = %#v, want %#v", got, want)
	}
}

func TestChunkTextSplitsOnWhitespace(t *testing.T) {
	got := ChunkText("alpha beta gamma delta", 11)
	want := []string{"alpha beta", "gamma delta"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ChunkText() = %#v, want %#v", got, want)
	}
	for _, chunk := range got {
		if len([]rune(chunk)) > 11 {
			t.Fatalf("chunk %q exceeds limit", chunk)
		}
	}
}

func TestChunkTextSplitsLongUnbrokenText(t *testing.T) {
	got := ChunkText("abcdefghij", 4)
	want := []string{"abcd", "efgh", "ij"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ChunkText() = %#v, want %#v", got, want)
	}
}

func TestChunkTextPrefersParagraphBreaks(t *testing.T) {
	input := "one two\n\nthree four"
	got := ChunkText(input, 14)
	want := []string{"one two", "three four"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ChunkText() = %#v, want %#v", got, want)
	}
	for _, chunk := range got {
		if len([]rune(chunk)) > 14 {
			t.Fatalf("chunk %q exceeds limit", chunk)
		}
	}
}

func TestChunkTextWithPolicyWordModeKeepsLaterWhitespaceBoundary(t *testing.T) {
	got := ChunkTextWithPolicy("one two\n\nthree four", TextChunkPolicy{Limit: 14, Mode: TextChunkModeWord})
	want := []string{"one two\n\nthree", "four"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ChunkTextWithPolicy() = %#v, want %#v", got, want)
	}
}

func TestChunkTextWithPolicyHardModeSplitsAtLimit(t *testing.T) {
	got := ChunkTextWithPolicy("abcdefghij", TextChunkPolicy{Limit: 4, Mode: TextChunkModeHard})
	want := []string{"abcd", "efgh", "ij"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ChunkTextWithPolicy() = %#v, want %#v", got, want)
	}
}

func TestNormalizeTextChunkMode(t *testing.T) {
	tests := map[string]string{
		"":          TextChunkModeParagraph,
		"paragraph": TextChunkModeParagraph,
		"WORD":      TextChunkModeWord,
		"hard":      TextChunkModeHard,
		"custom":    "custom",
	}
	for input, want := range tests {
		if got := NormalizeTextChunkMode(input); got != want {
			t.Fatalf("NormalizeTextChunkMode(%q) = %q, want %q", input, got, want)
		}
	}
}
