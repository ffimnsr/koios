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
