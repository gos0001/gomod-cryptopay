package sys_health

import "github.com/google/wire"

var Set = wire.NewSet(New, NewHTTPv1)
