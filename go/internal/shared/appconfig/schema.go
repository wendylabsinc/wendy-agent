package appconfig

import _ "embed"

//go:embed wendy.schema.json
var SchemaJSON string

//go:embed wendy-fleet.schema.json
var FleetSchemaJSON string
