package channels

import (
	"context"
	"strings"
	"time"
)

const (
	StreamQueueModeBurst    = "burst"
	StreamQueueModeThrottle = "throttle"
)

type StreamQueuePolicy struct {
	Mode     string
	Throttle time.Duration
}

func NormalizeStreamQueueMode(mode string) string {
	switch strings.ToLower(strings.TrimSpace(mode)) {
	case "", StreamQueueModeBurst:
		return StreamQueueModeBurst
	case StreamQueueModeThrottle:
		return StreamQueueModeThrottle
	default:
		return strings.ToLower(strings.TrimSpace(mode))
	}
}

func NormalizeStreamQueuePolicy(policy StreamQueuePolicy) StreamQueuePolicy {
	policy.Mode = NormalizeStreamQueueMode(policy.Mode)
	if policy.Throttle < 0 {
		policy.Throttle = 0
	}
	return policy
}

func SendChunked(ctx context.Context, chunks []string, policy StreamQueuePolicy, send func(index int, chunk string) error) error {
	policy = NormalizeStreamQueuePolicy(policy)
	for idx, chunk := range chunks {
		if idx > 0 && policy.Mode == StreamQueueModeThrottle && policy.Throttle > 0 {
			timer := time.NewTimer(policy.Throttle)
			select {
			case <-ctx.Done():
				if !timer.Stop() {
					<-timer.C
				}
				return ctx.Err()
			case <-timer.C:
			}
		}
		if err := send(idx, chunk); err != nil {
			return err
		}
	}
	return nil
}
