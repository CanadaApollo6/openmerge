// Package schema embeds the published repository configuration schema.
package schema

import _ "embed"

// V1 is the JSON Schema 2020-12 document printed by openmerge config schema.
//
//go:embed openmerge.v1.json
var V1 string
