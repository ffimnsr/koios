package handler

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/ffimnsr/koios/internal/skills"
)

func (h *Handler) rpcSkillCatalog(_ context.Context, wsc *wsConn, req *rpcRequest) {
	var p struct {
		IncludeBlocked  bool `json:"include_blocked"`
		IncludeShadowed bool `json:"include_shadowed"`
	}
	if err := decodeParams(req.Params, &p); err != nil {
		wsc.replyErr(req.ID, errCodeInvalidParams, err.Error())
		return
	}
	result, err := h.skillCatalog(p.IncludeBlocked, p.IncludeShadowed)
	if err != nil {
		wsc.replyErr(req.ID, errCodeServer, err.Error())
		return
	}
	wsc.reply(req.ID, result)
}

func (h *Handler) rpcSkillRefresh(_ context.Context, wsc *wsConn, req *rpcRequest) {
	var p struct{}
	if err := decodeParams(req.Params, &p); err != nil {
		wsc.replyErr(req.ID, errCodeInvalidParams, err.Error())
		return
	}
	result, err := h.skillRefresh()
	if err != nil {
		wsc.replyErr(req.ID, errCodeServer, err.Error())
		return
	}
	wsc.reply(req.ID, result)
}

func (h *Handler) rpcSkillScanInstall(_ context.Context, wsc *wsConn, req *rpcRequest) {
	var p struct {
		Path string `json:"path"`
	}
	if err := decodeParams(req.Params, &p); err != nil {
		wsc.replyErr(req.ID, errCodeInvalidParams, err.Error())
		return
	}
	result, err := h.skillScanInstall(wsc.peerID, p.Path)
	if err != nil {
		wsc.replyErr(req.ID, errCodeServer, err.Error())
		return
	}
	wsc.reply(req.ID, result)
}

func (h *Handler) rpcSkillInstall(ctx context.Context, wsc *wsConn, req *rpcRequest) {
	var p struct {
		Path    string `json:"path"`
		Confirm bool   `json:"confirm"`
	}
	if err := decodeParams(req.Params, &p); err != nil {
		wsc.replyErr(req.ID, errCodeInvalidParams, err.Error())
		return
	}
	result, err := h.skillInstall(ctx, wsc.peerID, p.Path, p.Confirm)
	if err != nil {
		wsc.replyErr(req.ID, errCodeServer, err.Error())
		return
	}
	wsc.reply(req.ID, result)
}

func (h *Handler) rpcSkillPending(_ context.Context, wsc *wsConn, req *rpcRequest) {
	var p struct{}
	if err := decodeParams(req.Params, &p); err != nil {
		wsc.replyErr(req.ID, errCodeInvalidParams, err.Error())
		return
	}
	wsc.reply(req.ID, map[string]any{"pending": h.approvals.list(wsc.peerID, skillApprovalFilter)})
}

func (h *Handler) rpcSkillApprove(ctx context.Context, wsc *wsConn, req *rpcRequest) {
	var p struct {
		ID string `json:"id"`
	}
	if err := decodeParams(req.Params, &p); err != nil {
		wsc.replyErr(req.ID, errCodeInvalidParams, err.Error())
		return
	}
	if strings.TrimSpace(p.ID) == "" {
		wsc.replyErr(req.ID, errCodeInvalidParams, "id is required")
		return
	}
	result, err := h.approvePendingAction(ctx, wsc.peerID, p.ID, skillApprovalFilter)
	if err != nil {
		wsc.replyErr(req.ID, errCodeServer, err.Error())
		return
	}
	wsc.reply(req.ID, result)
}

func (h *Handler) rpcSkillReject(_ context.Context, wsc *wsConn, req *rpcRequest) {
	var p struct {
		ID string `json:"id"`
	}
	if err := decodeParams(req.Params, &p); err != nil {
		wsc.replyErr(req.ID, errCodeInvalidParams, err.Error())
		return
	}
	if strings.TrimSpace(p.ID) == "" {
		wsc.replyErr(req.ID, errCodeInvalidParams, "id is required")
		return
	}
	if err := h.rejectPendingAction(wsc.peerID, p.ID, skillApprovalFilter); err != nil {
		wsc.replyErr(req.ID, errCodeServer, err.Error())
		return
	}
	wsc.reply(req.ID, map[string]bool{"ok": true})
}

func (h *Handler) skillCatalog(includeBlocked, includeShadowed bool) (map[string]any, error) {
	if h.skillManager == nil {
		return nil, fmt.Errorf("skills are not enabled")
	}
	snapshot, err := h.skillManager.CatalogDetails()
	if err != nil {
		return nil, err
	}
	entries := make([]skills.CatalogEntry, 0, len(snapshot.Entries))
	for _, entry := range snapshot.Entries {
		if entry.Status == "blocked" && !includeBlocked {
			continue
		}
		if entry.Status == "shadowed" && !includeShadowed {
			continue
		}
		entries = append(entries, entry)
	}
	snapshot.Entries = entries
	return map[string]any{
		"generation":      snapshot.Generation,
		"refreshed_at":    snapshot.RefreshedAt,
		"fingerprint":     snapshot.Fingerprint,
		"auto_refreshed":  snapshot.AutoRefreshed,
		"current_version": snapshot.CurrentVersion,
		"count":           len(snapshot.Entries),
		"entries":         snapshot.Entries,
	}, nil
}

func (h *Handler) skillRefresh() (map[string]any, error) {
	if h.skillManager == nil {
		return nil, fmt.Errorf("skills are not enabled")
	}
	snapshot, err := h.skillManager.Refresh()
	if err != nil {
		return nil, err
	}
	return map[string]any{
		"generation":      snapshot.Generation,
		"refreshed_at":    snapshot.RefreshedAt,
		"fingerprint":     snapshot.Fingerprint,
		"auto_refreshed":  snapshot.AutoRefreshed,
		"current_version": snapshot.CurrentVersion,
		"count":           len(snapshot.Entries),
		"entries":         snapshot.Entries,
	}, nil
}

func (h *Handler) skillScanInstall(peerID, source string) (map[string]any, error) {
	if h.skillManager == nil || h.workspaceStore == nil {
		return nil, fmt.Errorf("skills install scanning is not enabled")
	}
	resolved, err := h.resolveSkillWorkspacePath(peerID, source)
	if err != nil {
		return nil, err
	}
	scan, err := h.skillManager.ScanInstallSource(resolved)
	if err != nil {
		return nil, err
	}
	return map[string]any{
		"path":       source,
		"resolved":   resolved,
		"safe":       scan.Safe,
		"findings":   scan.Findings,
		"find_count": len(scan.Findings),
	}, nil
}

func (h *Handler) skillInstall(ctx context.Context, peerID, source string, confirm bool) (map[string]any, error) {
	if h.skillManager == nil || h.workspaceStore == nil {
		return nil, fmt.Errorf("skills install is not enabled")
	}
	resolved, err := h.resolveSkillWorkspacePath(peerID, source)
	if err != nil {
		return nil, err
	}
	scan, err := h.skillManager.ScanInstallSource(resolved)
	if err != nil {
		return nil, err
	}
	if !confirm {
		summary := fmt.Sprintf("Install skill from %s into managed skills", source)
		return h.requestApproval(peerID, pendingApproval{
			Kind:    "skill_install",
			Action:  "install",
			Summary: summary,
			Reason:  "managed skill install requires confirmation",
			Metadata: map[string]any{
				"path":     source,
				"resolved": resolved,
				"scan":     scan,
			},
		}, func(_ context.Context, _ string, approval pendingApproval) (map[string]any, error) {
			path, _ := approval.Metadata["resolved"].(string)
			installed, installErr := h.skillManager.InstallManagedSkill(path)
			if installErr != nil {
				return nil, installErr
			}
			return map[string]any{
				"status":   "installed",
				"approval": approval,
				"install":  installed,
			}, nil
		}), nil
	}
	installed, err := h.skillManager.InstallManagedSkill(resolved)
	if err != nil {
		return nil, err
	}
	return map[string]any{
		"status":  "installed",
		"install": installed,
	}, nil
}

func (h *Handler) resolveSkillWorkspacePath(peerID, source string) (string, error) {
	source = strings.TrimSpace(source)
	if source == "" {
		return "", fmt.Errorf("path is required")
	}
	if filepath.IsAbs(source) {
		clean := filepath.Clean(source)
		if h.identityDir != "" {
			root := filepath.Clean(h.identityDir)
			if clean == root || strings.HasPrefix(clean, root+string(filepath.Separator)) {
				return clean, nil
			}
		}
		return "", fmt.Errorf("absolute skill install paths must stay under the workspace root")
	}
	resolved, err := h.workspaceStore.Resolve(peerID, source)
	if err != nil {
		return "", err
	}
	return resolved, nil
}
