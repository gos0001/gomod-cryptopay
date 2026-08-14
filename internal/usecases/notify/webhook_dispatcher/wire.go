package webhook_dispatcher

import "github.com/google/wire"

var Set = wire.NewSet(LoadConfig, New, NewCron)
