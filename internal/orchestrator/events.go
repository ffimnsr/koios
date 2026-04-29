package orchestrator

import (
	"github.com/ffimnsr/koios/internal/eventbus"
	"github.com/ffimnsr/koios/internal/types"
)

func (o *Orchestrator) publishLifecycle(id, peerID, sessionKey, kind string, data map[string]any) {
	if o.bus == nil {
		return
	}
	o.bus.Publish(eventbus.Event{
		Kind:       kind,
		PeerID:     peerID,
		SessionKey: sessionKey,
		Source:     "orchestrator",
		RunID:      id,
		Data:       data,
	})
}

func (o *Orchestrator) publishChildProgress(orchID, peerID, sessionKey string, idx int, label, status string) {
	o.publishLifecycle(orchID, peerID, sessionKey, "orchestrator.child.progress", map[string]any{
		"child_index":  idx,
		"child_label":  label,
		"child_status": status,
	})
}

func (o *Orchestrator) publishSession(peerID, sessionKey, runID, content string) {
	if o.bus == nil {
		return
	}
	msg := types.Message{Role: "assistant", Content: content}
	o.bus.Publish(eventbus.Event{
		Kind:       "session.message",
		PeerID:     peerID,
		SessionKey: sessionKey,
		Source:     "orchestrator",
		RunID:      runID,
		Message:    &msg,
	})
}
