package invoice_create

import "github.com/google/wire"

var Set = wire.NewSet(New, NewHTTPv1)
