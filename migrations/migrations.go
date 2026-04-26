// Package migrations embeds SQL migration files into the binary.
// golang-migrate reads from this FS at startup — no external file path needed.
package migrations

import "embed"

//go:embed *.sql
var FS embed.FS
