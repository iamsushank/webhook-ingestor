// Package migrations exposes the SQL migration files as an embedded filesystem so
// the binary can run them on boot — no external migration tool or volume mount.
package migrations

import "embed"

//go:embed *.sql
var FS embed.FS
