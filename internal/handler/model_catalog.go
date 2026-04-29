package handler

// ModelProfileInfo captures one configured model profile for introspection tools.
type ModelProfileInfo struct {
	Name     string `json:"name"`
	Provider string `json:"provider,omitempty"`
	BaseURL  string `json:"base_url,omitempty"`
	Model    string `json:"model"`
}

// ModelCatalog is a read-only snapshot of the configured routing model state.
type ModelCatalog struct {
	Provider                 string             `json:"provider,omitempty"`
	BaseURL                  string             `json:"base_url,omitempty"`
	DefaultProfile           string             `json:"default_profile,omitempty"`
	LightweightModel         string             `json:"lightweight_model,omitempty"`
	FallbackModels           []string           `json:"fallback_models,omitempty"`
	Profiles                 []ModelProfileInfo `json:"profiles,omitempty"`
	LightweightWordThreshold int                `json:"lightweight_word_threshold,omitempty"`
}
