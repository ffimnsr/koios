package config

import _ "embed"

//go:embed templates/default_config.toml
var defaultTOMLTemplate string

//go:embed templates/encoded_config.toml
var encodedTOMLTemplate string
