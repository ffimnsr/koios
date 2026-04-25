package handler

import (
	"context"
	"log/slog"
	"strings"

	"github.com/ffimnsr/koios/internal/session"
	"github.com/ffimnsr/koios/internal/standing"
)

type presenceGetParams struct {
	PeerID string `json:"peer_id,omitempty"`
}

func (h *Handler) rpcPresenceGet(wsc *wsConn, req *rpcRequest) {
	var p presenceGetParams
	if err := decodeParams(req.Params, &p); err != nil {
		wsc.replyErr(req.ID, errCodeInvalidParams, err.Error())
		return
	}
	if p.PeerID != "" {
		state, ok := h.presence.Get(p.PeerID)
		wsc.reply(req.ID, map[string]any{
			"peer_id": p.PeerID,
			"found":   ok,
			"state":   state,
		})
		return
	}
	wsc.reply(req.ID, map[string]any{"states": h.presence.Snapshot()})
}

type presenceSetParams struct {
	Status string `json:"status,omitempty"`
	Typing *bool  `json:"typing,omitempty"`
}

func (h *Handler) rpcPresenceSet(wsc *wsConn, req *rpcRequest) {
	var p presenceSetParams
	if err := decodeParams(req.Params, &p); err != nil {
		wsc.replyErr(req.ID, errCodeInvalidParams, err.Error())
		return
	}
	typing := false
	if p.Typing != nil {
		typing = *p.Typing
	}
	state := h.presence.Set(wsc.peerID, p.Status, typing, "rpc")
	wsc.reply(req.ID, map[string]any{"state": state})
}

func (h *Handler) rpcStandingGet(_ context.Context, wsc *wsConn, req *rpcRequest) {
	workspace := ""
	peer := ""
	effective := ""
	defaultProfile := ""
	activeProfile := strings.TrimSpace(h.store.Policy(wsc.peerID).ActiveProfile)
	profileError := ""
	profiles := map[string]standing.Profile{}
	var resolvedProfile *standing.ResolvedProfile
	if h.standingManager != nil {
		workspace = h.standingManager.WorkspaceContent()
		var err error
		peer, err = h.standingManager.PeerContent(wsc.peerID)
		if err != nil {
			slog.Error("ws: standing get", "peer", wsc.peerID, "error", err)
			wsc.replyErr(req.ID, errCodeServer, "error loading standing orders")
			return
		}
		doc, err := h.standingManager.Document(wsc.peerID)
		if err != nil {
			slog.Error("ws: standing document", "peer", wsc.peerID, "error", err)
			wsc.replyErr(req.ID, errCodeServer, "error loading standing orders")
			return
		}
		if doc != nil {
			defaultProfile = doc.DefaultProfile
			profiles = doc.Profiles
		}
		effective, err = h.standingManager.EffectiveContentForProfile(wsc.peerID, activeProfile)
		if err != nil {
			profileError = err.Error()
			parts := make([]string, 0, 2)
			if workspace != "" {
				parts = append(parts, workspace)
			}
			if peer != "" {
				parts = append(parts, peer)
			}
			effective = strings.TrimSpace(strings.Join(parts, "\n\n"))
		} else {
			resolvedProfile, err = h.standingManager.ResolveProfile(wsc.peerID, activeProfile)
			if err != nil {
				profileError = err.Error()
			}
		}
	}
	wsc.reply(req.ID, map[string]any{
		"peer_id":           wsc.peerID,
		"workspace_content": workspace,
		"peer_content":      peer,
		"effective_content": effective,
		"default_profile":   defaultProfile,
		"active_profile":    activeProfile,
		"resolved_profile":  resolvedProfile,
		"profiles":          profiles,
		"profile_error":     profileError,
		"writable":          h.standingManager != nil && h.standingManager.Writable(),
	})
}

func (h *Handler) rpcStandingSet(_ context.Context, wsc *wsConn, req *rpcRequest) {
	if h.standingManager == nil || !h.standingManager.Writable() {
		wsc.replyErr(req.ID, errCodeServer, "standing order persistence is not enabled")
		return
	}
	var p struct {
		Content string `json:"content"`
	}
	if err := decodeParams(req.Params, &p); err != nil {
		wsc.replyErr(req.ID, errCodeInvalidParams, err.Error())
		return
	}
	doc, err := h.standingManager.Store().Save(wsc.peerID, p.Content)
	if err != nil {
		slog.Error("ws: standing set", "peer", wsc.peerID, "error", err)
		wsc.replyErr(req.ID, errCodeServer, "error saving standing orders")
		return
	}
	wsc.reply(req.ID, doc)
}

func (h *Handler) rpcStandingClear(_ context.Context, wsc *wsConn, req *rpcRequest) {
	if h.standingManager == nil || !h.standingManager.Writable() {
		wsc.replyErr(req.ID, errCodeServer, "standing order persistence is not enabled")
		return
	}
	if err := h.standingManager.Store().Delete(wsc.peerID); err != nil {
		slog.Error("ws: standing clear", "peer", wsc.peerID, "error", err)
		wsc.replyErr(req.ID, errCodeServer, "error clearing standing orders")
		return
	}
	wsc.reply(req.ID, map[string]bool{"ok": true})
}

func (h *Handler) rpcStandingProfileSet(_ context.Context, wsc *wsConn, req *rpcRequest) {
	if h.standingManager == nil || !h.standingManager.Writable() {
		wsc.replyErr(req.ID, errCodeServer, "standing order persistence is not enabled")
		return
	}
	var p struct {
		Name          string   `json:"name"`
		Content       string   `json:"content"`
		ToolProfile   string   `json:"tool_profile"`
		ToolsAllow    []string `json:"tools_allow"`
		ToolsDeny     []string `json:"tools_deny"`
		ResponseStyle string   `json:"response_style"`
		ThinkLevel    string   `json:"think_level"`
		VerboseMode   *bool    `json:"verbose_mode"`
		TraceMode     *bool    `json:"trace_mode"`
		MakeDefault   bool     `json:"make_default"`
	}
	if err := decodeParams(req.Params, &p); err != nil {
		wsc.replyErr(req.ID, errCodeInvalidParams, err.Error())
		return
	}
	name := strings.TrimSpace(p.Name)
	if name == "" {
		wsc.replyErr(req.ID, errCodeInvalidParams, "name is required")
		return
	}
	if strings.TrimSpace(p.ThinkLevel) != "" && !validThinkLevels[strings.TrimSpace(p.ThinkLevel)] {
		wsc.replyErr(req.ID, errCodeInvalidParams, "invalid think level")
		return
	}
	doc, err := h.standingManager.Document(wsc.peerID)
	if err != nil {
		wsc.replyErr(req.ID, errCodeServer, "error loading standing orders")
		return
	}
	if doc == nil {
		doc = &standing.Document{PeerID: wsc.peerID}
	}
	if doc.Profiles == nil {
		doc.Profiles = make(map[string]standing.Profile)
	}
	profile := standing.Profile{
		Content:       p.Content,
		ToolProfile:   p.ToolProfile,
		ToolsAllow:    p.ToolsAllow,
		ToolsDeny:     p.ToolsDeny,
		ResponseStyle: p.ResponseStyle,
		ThinkLevel:    p.ThinkLevel,
		VerboseMode:   cloneOptionalBool(p.VerboseMode),
		TraceMode:     cloneOptionalBool(p.TraceMode),
	}
	doc.Profiles[name] = profile
	if p.MakeDefault {
		doc.DefaultProfile = name
	}
	saved, err := h.standingManager.Store().SaveDocument(doc)
	if err != nil {
		slog.Error("ws: standing profile set", "peer", wsc.peerID, "profile", name, "error", err)
		wsc.replyErr(req.ID, errCodeServer, "error saving standing profile")
		return
	}
	wsc.reply(req.ID, map[string]any{
		"ok":              true,
		"profile_name":    name,
		"profile":         saved.Profiles[name],
		"default_profile": saved.DefaultProfile,
	})
}

func (h *Handler) rpcStandingProfileDelete(_ context.Context, wsc *wsConn, req *rpcRequest) {
	if h.standingManager == nil || !h.standingManager.Writable() {
		wsc.replyErr(req.ID, errCodeServer, "standing order persistence is not enabled")
		return
	}
	var p struct {
		Name string `json:"name"`
	}
	if err := decodeParams(req.Params, &p); err != nil {
		wsc.replyErr(req.ID, errCodeInvalidParams, err.Error())
		return
	}
	name := strings.TrimSpace(p.Name)
	if name == "" {
		wsc.replyErr(req.ID, errCodeInvalidParams, "name is required")
		return
	}
	doc, err := h.standingManager.Document(wsc.peerID)
	if err != nil {
		wsc.replyErr(req.ID, errCodeServer, "error loading standing orders")
		return
	}
	if doc == nil || doc.Profiles == nil {
		wsc.replyErr(req.ID, errCodeInvalidParams, "standing profile not found")
		return
	}
	if _, ok := doc.Profiles[name]; !ok {
		wsc.replyErr(req.ID, errCodeInvalidParams, "standing profile not found")
		return
	}
	delete(doc.Profiles, name)
	if doc.DefaultProfile == name {
		doc.DefaultProfile = ""
	}
	if _, err := h.standingManager.Store().SaveDocument(doc); err != nil {
		slog.Error("ws: standing profile delete", "peer", wsc.peerID, "profile", name, "error", err)
		wsc.replyErr(req.ID, errCodeServer, "error deleting standing profile")
		return
	}
	if h.store.Policy(wsc.peerID).ActiveProfile == name {
		_ = h.store.PatchPolicy(wsc.peerID, func(policy *session.SessionPolicy) {
			policy.ActiveProfile = ""
		})
	}
	wsc.reply(req.ID, map[string]any{"ok": true, "profile_name": name})
}

func (h *Handler) rpcStandingProfileActivate(_ context.Context, wsc *wsConn, req *rpcRequest) {
	var p struct {
		Name       string `json:"name"`
		SessionKey string `json:"session_key,omitempty"`
	}
	if err := decodeParams(req.Params, &p); err != nil {
		wsc.replyErr(req.ID, errCodeInvalidParams, err.Error())
		return
	}
	sessionKey := strings.TrimSpace(p.SessionKey)
	if sessionKey == "" {
		sessionKey = wsc.peerID
	}
	if sessionKey != wsc.peerID && !strings.HasPrefix(sessionKey, wsc.peerID+"::") {
		wsc.replyErr(req.ID, errCodeInvalidParams, "session_key is not accessible to this peer")
		return
	}
	name := strings.TrimSpace(p.Name)
	if name != "" {
		if _, err := h.standingManager.ResolveProfile(wsc.peerID, name); err != nil {
			wsc.replyErr(req.ID, errCodeInvalidParams, err.Error())
			return
		}
	}
	if err := h.store.PatchPolicy(sessionKey, func(policy *session.SessionPolicy) {
		policy.ActiveProfile = name
	}); err != nil {
		wsc.replyErr(req.ID, errCodeServer, "could not persist policy: "+err.Error())
		return
	}
	wsc.reply(req.ID, map[string]any{
		"ok":             true,
		"session_key":    sessionKey,
		"active_profile": name,
	})
}

func cloneOptionalBool(value *bool) *bool {
	if value == nil {
		return nil
	}
	cloned := *value
	return &cloned
}
