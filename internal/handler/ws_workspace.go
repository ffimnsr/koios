package handler

import (
	"context"
	"strings"
)

// ── workspace ────────────────────────────────────────────────────────────────

func (h *Handler) rpcWorkspaceList(_ context.Context, wsc *wsConn, req *rpcRequest) {
	var p struct {
		Path      string `json:"path,omitempty"`
		Recursive bool   `json:"recursive,omitempty"`
		Limit     int    `json:"limit,omitempty"`
	}
	if err := decodeParams(req.Params, &p); err != nil {
		wsc.replyErr(req.ID, errCodeInvalidParams, err.Error())
		return
	}
	if strings.TrimSpace(p.Path) == "" {
		p.Path = "."
	}
	result, err := h.workspaceList(wsc.peerID, p.Path, p.Recursive, p.Limit)
	if err != nil {
		wsc.replyErr(req.ID, errCodeServer, err.Error())
		return
	}
	wsc.reply(req.ID, result)
}

func (h *Handler) rpcWorkspaceRead(_ context.Context, wsc *wsConn, req *rpcRequest) {
	var p struct {
		Path      string `json:"path"`
		StartLine int    `json:"start_line,omitempty"`
		EndLine   int    `json:"end_line,omitempty"`
	}
	if err := decodeParams(req.Params, &p); err != nil {
		wsc.replyErr(req.ID, errCodeInvalidParams, err.Error())
		return
	}
	if strings.TrimSpace(p.Path) == "" {
		wsc.replyErr(req.ID, errCodeInvalidParams, "path is required")
		return
	}
	result, err := h.workspaceRead(wsc.peerID, p.Path, p.StartLine, p.EndLine)
	if err != nil {
		wsc.replyErr(req.ID, errCodeServer, err.Error())
		return
	}
	wsc.reply(req.ID, result)
}

func (h *Handler) rpcWorkspaceHead(_ context.Context, wsc *wsConn, req *rpcRequest) {
	var p struct {
		Path  string `json:"path"`
		Lines int    `json:"lines,omitempty"`
	}
	if err := decodeParams(req.Params, &p); err != nil {
		wsc.replyErr(req.ID, errCodeInvalidParams, err.Error())
		return
	}
	if p.Lines == 0 {
		p.Lines = 10
	}
	result, err := h.workspaceHead(wsc.peerID, p.Path, p.Lines)
	if err != nil {
		wsc.replyErr(req.ID, errCodeServer, err.Error())
		return
	}
	wsc.reply(req.ID, result)
}

func (h *Handler) rpcWorkspaceTail(_ context.Context, wsc *wsConn, req *rpcRequest) {
	var p struct {
		Path  string `json:"path"`
		Lines int    `json:"lines,omitempty"`
	}
	if err := decodeParams(req.Params, &p); err != nil {
		wsc.replyErr(req.ID, errCodeInvalidParams, err.Error())
		return
	}
	if p.Lines == 0 {
		p.Lines = 10
	}
	result, err := h.workspaceTail(wsc.peerID, p.Path, p.Lines)
	if err != nil {
		wsc.replyErr(req.ID, errCodeServer, err.Error())
		return
	}
	wsc.reply(req.ID, result)
}

func (h *Handler) rpcWorkspaceGrep(_ context.Context, wsc *wsConn, req *rpcRequest) {
	var p struct {
		Path          string `json:"path,omitempty"`
		Pattern       string `json:"pattern"`
		Recursive     *bool  `json:"recursive,omitempty"`
		Limit         int    `json:"limit,omitempty"`
		CaseSensitive *bool  `json:"case_sensitive,omitempty"`
		Regexp        bool   `json:"regexp,omitempty"`
	}
	if err := decodeParams(req.Params, &p); err != nil {
		wsc.replyErr(req.ID, errCodeInvalidParams, err.Error())
		return
	}
	recursive := true
	if p.Recursive != nil {
		recursive = *p.Recursive
	}
	caseSensitive := true
	if p.CaseSensitive != nil {
		caseSensitive = *p.CaseSensitive
	}
	result, err := h.workspaceGrep(wsc.peerID, p.Path, p.Pattern, recursive, p.Limit, caseSensitive, p.Regexp)
	if err != nil {
		wsc.replyErr(req.ID, errCodeServer, err.Error())
		return
	}
	wsc.reply(req.ID, result)
}

func (h *Handler) rpcWorkspaceSort(_ context.Context, wsc *wsConn, req *rpcRequest) {
	var p struct {
		Path          string `json:"path"`
		Reverse       bool   `json:"reverse,omitempty"`
		CaseSensitive *bool  `json:"case_sensitive,omitempty"`
	}
	if err := decodeParams(req.Params, &p); err != nil {
		wsc.replyErr(req.ID, errCodeInvalidParams, err.Error())
		return
	}
	caseSensitive := true
	if p.CaseSensitive != nil {
		caseSensitive = *p.CaseSensitive
	}
	result, err := h.workspaceSort(wsc.peerID, p.Path, p.Reverse, caseSensitive)
	if err != nil {
		wsc.replyErr(req.ID, errCodeServer, err.Error())
		return
	}
	wsc.reply(req.ID, result)
}

func (h *Handler) rpcWorkspaceUniq(_ context.Context, wsc *wsConn, req *rpcRequest) {
	var p struct {
		Path          string `json:"path"`
		Count         bool   `json:"count,omitempty"`
		CaseSensitive *bool  `json:"case_sensitive,omitempty"`
	}
	if err := decodeParams(req.Params, &p); err != nil {
		wsc.replyErr(req.ID, errCodeInvalidParams, err.Error())
		return
	}
	caseSensitive := true
	if p.CaseSensitive != nil {
		caseSensitive = *p.CaseSensitive
	}
	result, err := h.workspaceUniq(wsc.peerID, p.Path, p.Count, caseSensitive)
	if err != nil {
		wsc.replyErr(req.ID, errCodeServer, err.Error())
		return
	}
	wsc.reply(req.ID, result)
}

func (h *Handler) rpcWorkspaceDiff(_ context.Context, wsc *wsConn, req *rpcRequest) {
	var p struct {
		Path      string `json:"path"`
		OtherPath string `json:"other_path,omitempty"`
		Content   string `json:"content,omitempty"`
		Context   int    `json:"context,omitempty"`
	}
	if err := decodeParams(req.Params, &p); err != nil {
		wsc.replyErr(req.ID, errCodeInvalidParams, err.Error())
		return
	}
	if p.Context == 0 {
		p.Context = 3
	}
	result, err := h.workspaceDiff(wsc.peerID, p.Path, p.OtherPath, p.Content, p.Context)
	if err != nil {
		wsc.replyErr(req.ID, errCodeServer, err.Error())
		return
	}
	wsc.reply(req.ID, result)
}

func (h *Handler) rpcWorkspaceWrite(_ context.Context, wsc *wsConn, req *rpcRequest) {
	var p struct {
		Path    string `json:"path"`
		Content string `json:"content"`
		Append  bool   `json:"append,omitempty"`
	}
	if err := decodeParams(req.Params, &p); err != nil {
		wsc.replyErr(req.ID, errCodeInvalidParams, err.Error())
		return
	}
	if strings.TrimSpace(p.Path) == "" {
		wsc.replyErr(req.ID, errCodeInvalidParams, "path is required")
		return
	}
	result, err := h.workspaceWrite(wsc.peerID, p.Path, p.Content, p.Append)
	if err != nil {
		wsc.replyErr(req.ID, errCodeServer, err.Error())
		return
	}
	wsc.reply(req.ID, result)
}

func (h *Handler) rpcWorkspaceEdit(_ context.Context, wsc *wsConn, req *rpcRequest) {
	var p struct {
		Path       string `json:"path"`
		OldText    string `json:"old_text"`
		NewText    string `json:"new_text"`
		ReplaceAll bool   `json:"replace_all,omitempty"`
	}
	if err := decodeParams(req.Params, &p); err != nil {
		wsc.replyErr(req.ID, errCodeInvalidParams, err.Error())
		return
	}
	result, err := h.workspaceEdit(wsc.peerID, p.Path, p.OldText, p.NewText, p.ReplaceAll)
	if err != nil {
		wsc.replyErr(req.ID, errCodeServer, err.Error())
		return
	}
	wsc.reply(req.ID, result)
}

func (h *Handler) rpcWorkspaceMkdir(_ context.Context, wsc *wsConn, req *rpcRequest) {
	var p struct {
		Path string `json:"path"`
	}
	if err := decodeParams(req.Params, &p); err != nil {
		wsc.replyErr(req.ID, errCodeInvalidParams, err.Error())
		return
	}
	if strings.TrimSpace(p.Path) == "" {
		wsc.replyErr(req.ID, errCodeInvalidParams, "path is required")
		return
	}
	result, err := h.workspaceMkdir(wsc.peerID, p.Path)
	if err != nil {
		wsc.replyErr(req.ID, errCodeServer, err.Error())
		return
	}
	wsc.reply(req.ID, result)
}

func (h *Handler) rpcWorkspaceDelete(_ context.Context, wsc *wsConn, req *rpcRequest) {
	var p struct {
		Path      string `json:"path"`
		Recursive bool   `json:"recursive,omitempty"`
	}
	if err := decodeParams(req.Params, &p); err != nil {
		wsc.replyErr(req.ID, errCodeInvalidParams, err.Error())
		return
	}
	if strings.TrimSpace(p.Path) == "" {
		wsc.replyErr(req.ID, errCodeInvalidParams, "path is required")
		return
	}
	result, err := h.workspaceDelete(wsc.peerID, p.Path, p.Recursive)
	if err != nil {
		wsc.replyErr(req.ID, errCodeServer, err.Error())
		return
	}
	wsc.reply(req.ID, result)
}
