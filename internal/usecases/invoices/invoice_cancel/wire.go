package invoice_cancel

import "github.com/google/wire"

var Set = wire.NewSet(New, NewHTTPv1)
