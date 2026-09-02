package migrations

import "embed"

// FS contains ordered, immutable database migrations.
//
//go:embed *.sql
var FS embed.FS
