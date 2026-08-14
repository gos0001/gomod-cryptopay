// Package schema embeds the SQL that defines this service's tables.
//
// It lives in the schema directory itself because go:embed cannot reach outside
// its own package directory. Embedding is what lets the published container
// image create its own tables: the image ships one static binary, so anything
// not compiled into it simply is not there at runtime.
//
// The same file is what sqlc validates queries against, so the schema the
// service creates and the schema the generated code assumes cannot drift.
package schema

import (
	_ "embed"

	"github.com/google/wire"

	"github.com/gos0001/gomod-cryptopay/pkg/dbschema"
)

//go:embed schema.sql
var sql string

// Definition returns the full schema. Every statement in it is idempotent, so
// it is safe to run on every start.
func Definition() dbschema.Definition { return dbschema.Definition(sql) }

var Set = wire.NewSet(Definition)
