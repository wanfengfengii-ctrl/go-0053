package store

import _ "embed"

//go:embed migrations/0001_init.sql
var schemaSQL string
