package channels

import (
	"fmt"
	"strings"
	"time"
)

const (
	dmPolicyOpen    = "open"
	dmPolicyPairing = "pairing"
	dmPolicyClosed  = "closed"
)

type DirectMessageAuthRequest struct {
	Channel        string
	Policy         string
	SubjectID      string
	ConversationID string
	Username       string
	DisplayName    string
	Metadata       map[string]string
	PairingCodeTTL time.Duration
	Allowed        bool
	Store          *BindingStore
	ClosedMessage  string
}

type DirectMessageAuthDecision struct {
	Allowed   bool
	ReplyText string
	Pending   *PendingBinding
}

func NormalizeDirectMessagePolicy(policy string) string {
	switch strings.ToLower(strings.TrimSpace(policy)) {
	case dmPolicyPairing:
		return dmPolicyPairing
	case dmPolicyClosed:
		return dmPolicyClosed
	default:
		return dmPolicyOpen
	}
}

func AuthorizeDirectMessage(req DirectMessageAuthRequest) (DirectMessageAuthDecision, error) {
	if req.Allowed {
		return DirectMessageAuthDecision{Allowed: true}, nil
	}

	subjectID := strings.TrimSpace(req.SubjectID)
	if subjectID == "" {
		return DirectMessageAuthDecision{}, nil
	}

	if req.Store != nil {
		approved, err := req.Store.IsApproved(req.Channel, subjectID)
		if err != nil {
			return DirectMessageAuthDecision{}, err
		}
		if approved {
			return DirectMessageAuthDecision{Allowed: true}, nil
		}
	}

	switch NormalizeDirectMessagePolicy(req.Policy) {
	case dmPolicyOpen:
		return DirectMessageAuthDecision{Allowed: true}, nil
	case dmPolicyClosed:
		message := strings.TrimSpace(req.ClosedMessage)
		if message == "" {
			message = "Direct messages are closed for this bot."
		}
		return DirectMessageAuthDecision{ReplyText: message}, nil
	case dmPolicyPairing:
		if req.Store == nil {
			return DirectMessageAuthDecision{}, fmt.Errorf("channel %q requires a binding store for dm pairing", strings.TrimSpace(req.Channel))
		}
		pending, err := req.Store.EnsurePending(BindingRequest{
			Channel:        req.Channel,
			SubjectID:      subjectID,
			ConversationID: strings.TrimSpace(req.ConversationID),
			Username:       strings.TrimSpace(req.Username),
			DisplayName:    strings.TrimSpace(req.DisplayName),
			Metadata:       req.Metadata,
			TTL:            req.PairingCodeTTL,
		})
		if err != nil {
			return DirectMessageAuthDecision{}, err
		}
		return DirectMessageAuthDecision{
			Pending: pending,
			ReplyText: fmt.Sprintf(
				"This bot requires approval before direct messages are accepted. Share binding code %s with an operator and ask them to run: koios pairing approve %s %s",
				pending.Code,
				strings.TrimSpace(req.Channel),
				pending.Code,
			),
		}, nil
	default:
		return DirectMessageAuthDecision{Allowed: true}, nil
	}
}
