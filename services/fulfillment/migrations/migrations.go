// Package migrations embeds the fulfillment database schema in the binary.
package migrations

import "embed"

//go:embed *.sql
var FS embed.FS
