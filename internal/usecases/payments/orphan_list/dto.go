package orphan_list

import "github.com/gos0001/gomod-cryptopay/internal/view"

const (
	defaultLimit = 50
	maxLimit     = 200
)

type Input struct {
	Limit int32 `json:"-"`
}

// Validate clamps rather than refuses: an oversized limit is a client bug, and
// failing the request helps nobody.
//
// No cursor here, unlike invoices. Orphans are rare by construction and read by
// a human during reconciliation, not walked in batches — newest first with a
// limit is the whole requirement.
func (in *Input) Validate() error {
	if in.Limit <= 0 {
		in.Limit = defaultLimit
	}
	if in.Limit > maxLimit {
		in.Limit = maxLimit
	}
	return nil
}

type Output struct {
	Orphans []view.Orphan `json:"orphans"`
}
