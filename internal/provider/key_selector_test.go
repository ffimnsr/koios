package provider

import (
	"context"
	"fmt"
	"testing"

	"github.com/ffimnsr/koios/internal/types"
)

func TestCredentialSelectorStickyLeastUsedAndUnhealthyFallback(t *testing.T) {
	selector := newCredentialSelector("openai", []string{"key-a", "key-b"})
	ctxA := types.WithRequestIdentity(context.Background(), types.RequestIdentity{SessionKey: "alice::chat"})
	ctxB := types.WithRequestIdentity(context.Background(), types.RequestIdentity{SessionKey: "bob::chat"})
	ctxC := types.WithRequestIdentity(context.Background(), types.RequestIdentity{SessionKey: "cara::chat"})

	keyA, reportA := selector.Select(ctxA, &types.ChatRequest{Model: "gpt-4o"})
	if keyA != "key-a" {
		t.Fatalf("first selected key = %q, want key-a", keyA)
	}
	keyB, _ := selector.Select(ctxB, &types.ChatRequest{Model: "gpt-4o"})
	if keyB != "key-b" {
		t.Fatalf("second selected key = %q, want key-b", keyB)
	}
	keyA2, _ := selector.Select(ctxA, &types.ChatRequest{Model: "gpt-4o"})
	if keyA2 != keyA {
		t.Fatalf("sticky key = %q, want %q", keyA2, keyA)
	}

	reportA(fmt.Errorf("upstream 429: rate limit"))
	keyC, _ := selector.Select(ctxC, &types.ChatRequest{Model: "gpt-4o"})
	if keyC != "key-b" {
		t.Fatalf("healthy fallback key = %q, want key-b", keyC)
	}
}
