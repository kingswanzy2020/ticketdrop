// Package migrations embeds the orders database schema in the binary.
package migrations

import "embed"

//go:embed *.sql
var FS embed.FS
