package cli

import _ "embed"

//go:embed templates/host_status_long.txt
var hostStatusLongHelp string

//go:embed templates/migrate_long.txt
var migrateLongHelp string

//go:embed templates/model_set_long.txt
var modelSetLongHelp string

//go:embed templates/init_wizard_config.toml
var initWizardConfigTemplate string

//go:embed templates/bootstrap/AGENTS.md
var scaffoldAgentsTemplate string

//go:embed templates/bootstrap/SOUL.md
var scaffoldSoulTemplate string

//go:embed templates/bootstrap/USER.md
var scaffoldUserTemplate string

//go:embed templates/bootstrap/IDENTITY.md
var scaffoldIdentityTemplate string

//go:embed templates/bootstrap/BOOTSTRAP.md
var scaffoldBootstrapTemplate string

//go:embed templates/bootstrap/TOOLS.md
var scaffoldToolsTemplate string

//go:embed templates/bootstrap/HEARTBEAT.md
var scaffoldHeartbeatTemplate string
