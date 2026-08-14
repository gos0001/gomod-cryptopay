package asset_seeder

import "github.com/google/wire"

var Set = wire.NewSet(LoadConfig, New, NewBootstrap)
