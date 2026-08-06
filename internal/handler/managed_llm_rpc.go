package handler

import (
	"context"
	"fmt"
	"strings"

	"github.com/ffimnsr/koios/internal/agent"
)

// ManagedLLMProfileInfo is the safe public view of an operator-configured LLM
// profile. Credentials and endpoint URLs deliberately never cross the RPC boundary.
type ManagedLLMProfileInfo struct {
	Name     string `json:"name"`
	Provider string `json:"provider"`
	Model    string `json:"model"`
}

func (h *Handler) rpcManagedLLMProfileList(ctx context.Context, wsc *wsConn, req *rpcRequest) {
	activeProfile := ""
	if h.preferenceStore != nil {
		if pref, err := h.preferenceStore.Get(ctx, wsc.peerID, agent.ManagedProfilePreferenceKey, "global"); err == nil {
			activeProfile = strings.TrimSpace(pref.Value)
		}
	}
	wsc.reply(req.ID, map[string]any{
		"profiles":       h.managedLLMProfiles,
		"count":          len(h.managedLLMProfiles),
		"active_profile": activeProfile,
	})
}

type managedLLMProfileActivateParams struct {
	Name string `json:"name"`
}

// rpcManagedLLMProfileActivate selects an operator-managed profile for this
// peer. The profile remains read-only: this only stores a peer-local selector.
func (h *Handler) rpcManagedLLMProfileActivate(ctx context.Context, wsc *wsConn, req *rpcRequest) {
	var p managedLLMProfileActivateParams
	if err := decodeParams(req.Params, &p); err != nil {
		wsc.replyErr(req.ID, errCodeInvalidParams, err.Error())
		return
	}
	name := strings.TrimSpace(p.Name)
	if name == "" {
		wsc.replyErr(req.ID, errCodeInvalidParams, "name is required")
		return
	}
	if !h.hasManagedLLMProfile(name) {
		wsc.replyErr(req.ID, errCodeInvalidParams, fmt.Sprintf("managed LLM profile %q not found", name))
		return
	}
	if h.preferenceStore == nil {
		wsc.replyErr(req.ID, errCodeServer, "managed LLM profile selection is not enabled")
		return
	}
	if _, err := h.preferenceStore.Set(ctx, wsc.peerID, preferencesInput(agent.ManagedProfilePreferenceKey, name)); err != nil {
		wsc.replyErr(req.ID, errCodeServer, fmt.Sprintf("activate managed profile: %s", err))
		return
	}
	if err := h.preferenceStore.Delete(ctx, wsc.peerID, "peer.llm.default_provider_profile", "global"); err != nil {
		wsc.replyErr(req.ID, errCodeServer, fmt.Sprintf("clear BYOK profile selection: %s", err))
		return
	}
	wsc.reply(req.ID, map[string]any{
		"ok":             true,
		"active_profile": name,
	})
}

func (h *Handler) hasManagedLLMProfile(name string) bool {
	for _, profile := range h.managedLLMProfiles {
		if profile.Name == name {
			return true
		}
	}
	return false
}
