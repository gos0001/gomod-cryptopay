package tron_watcher

import "github.com/google/wire"

var Set = wire.NewSet(LoadConfig, New, NewCron)
