// Package migrations embeds the inventory database schema in the binary.
package migrations

import "embed"

//go:embed *.sql
var FS embed.FS
