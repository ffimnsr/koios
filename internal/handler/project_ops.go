package handler

import (
	"context"
	"fmt"

	"github.com/ffimnsr/koios/internal/projects"
)

func projectPatch(title, description *string, labels *[]string) projects.Patch {
	return projects.Patch{Title: title, Description: description, Labels: labels}
}

func (h *Handler) projectCreate(peerID string, input projects.Input, ctx context.Context) (map[string]any, error) {
	if h.projectStore == nil {
		return nil, fmt.Errorf("projects are not enabled")
	}
	p, err := h.projectStore.Create(ctx, peerID, input)
	if err != nil {
		return nil, err
	}
	return map[string]any{"ok": true, "project": p}, nil
}

func (h *Handler) projectList(peerID string, limit int, status projects.ProjectStatus, ctx context.Context) (map[string]any, error) {
	if h.projectStore == nil {
		return nil, fmt.Errorf("projects are not enabled")
	}
	list, err := h.projectStore.List(ctx, peerID, limit, status)
	if err != nil {
		return nil, err
	}
	return map[string]any{"ok": true, "count": len(list), "projects": list}, nil
}

func (h *Handler) projectStatus(peerID, id string, ctx context.Context) (map[string]any, error) {
	if h.projectStore == nil {
		return nil, fmt.Errorf("projects are not enabled")
	}
	p, err := h.projectStore.Get(ctx, peerID, id)
	if err != nil {
		return nil, err
	}
	return map[string]any{"ok": true, "project": p}, nil
}

func (h *Handler) projectUpdate(peerID, id string, patch projects.Patch, ctx context.Context) (map[string]any, error) {
	if h.projectStore == nil {
		return nil, fmt.Errorf("projects are not enabled")
	}
	p, err := h.projectStore.Update(ctx, peerID, id, patch)
	if err != nil {
		return nil, err
	}
	return map[string]any{"ok": true, "project": p}, nil
}

func (h *Handler) projectLinkTask(peerID, id, taskID string, ctx context.Context) (map[string]any, error) {
	if h.projectStore == nil {
		return nil, fmt.Errorf("projects are not enabled")
	}
	p, err := h.projectStore.LinkTask(ctx, peerID, id, taskID)
	if err != nil {
		return nil, err
	}
	return map[string]any{"ok": true, "project": p}, nil
}

func (h *Handler) projectUnlinkTask(peerID, id, taskID string, ctx context.Context) (map[string]any, error) {
	if h.projectStore == nil {
		return nil, fmt.Errorf("projects are not enabled")
	}
	p, err := h.projectStore.UnlinkTask(ctx, peerID, id, taskID)
	if err != nil {
		return nil, err
	}
	return map[string]any{"ok": true, "project": p}, nil
}

func (h *Handler) projectArchive(peerID, id string, ctx context.Context) (map[string]any, error) {
	if h.projectStore == nil {
		return nil, fmt.Errorf("projects are not enabled")
	}
	p, err := h.projectStore.Archive(ctx, peerID, id)
	if err != nil {
		return nil, err
	}
	return map[string]any{"ok": true, "project": p}, nil
}

func (h *Handler) projectUnarchive(peerID, id string, ctx context.Context) (map[string]any, error) {
	if h.projectStore == nil {
		return nil, fmt.Errorf("projects are not enabled")
	}
	p, err := h.projectStore.Unarchive(ctx, peerID, id)
	if err != nil {
		return nil, err
	}
	return map[string]any{"ok": true, "project": p}, nil
}
