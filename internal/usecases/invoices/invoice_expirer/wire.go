package invoice_expirer

import "github.com/google/wire"

var Set = wire.NewSet(New, NewCron)
