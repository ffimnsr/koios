package handler

import (
	"context"
	"time"
)

type builtinTimePlugin struct{}

func (builtinTimePlugin) Descriptor() pluginDescriptor {
	return pluginDescriptor{
		ID:          "core.time",
		Description: "Built-in clock tools for runtime timestamps.",
	}
}

func (builtinTimePlugin) Register(registrar *pluginRegistrar) error {
	return registrar.RegisterTool(pluginTool{
		Name:        "time.now",
		Description: "Get the current UTC time from the server.",
		Parameters: mustJSONSchema(map[string]any{
			"type":                 "object",
			"properties":           map[string]any{},
			"additionalProperties": false,
		}),
		ArgHint: `{}`,
		Execute: func(_ context.Context, _ pluginToolContext) (any, error) {
			return map[string]string{"utc": time.Now().UTC().Format(time.RFC3339)}, nil
		},
	})
}
