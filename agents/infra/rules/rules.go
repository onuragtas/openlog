// Package rules embeds the starter discovery rule catalog. Each *.yaml file
// holds one rule; see README.md for the schema.
package rules

import "embed"

// FS contains the embedded rule files.
//
//go:embed *.yaml
var FS embed.FS
