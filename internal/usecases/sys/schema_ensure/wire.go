package schema_ensure

import "github.com/google/wire"

var Set = wire.NewSet(New, NewBootstrap)
