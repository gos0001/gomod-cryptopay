package tron

import "github.com/google/wire"

// Set is not yet consumed by cmd/wire.go — wire rejects a provider set nothing
// uses. It enters the graph with the TRON watcher.
var Set = wire.NewSet(LoadConfig, New)
