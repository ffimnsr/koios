package cli

import _ "embed"

//go:embed templates/tests/health_status_config.toml
var healthStatusConfigTemplate string

//go:embed templates/tests/agent_oneshot_config.toml
var agentOneShotConfigTemplate string
