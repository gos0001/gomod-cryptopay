package invoice_cancel

import (
	"fmt"

	"github.com/google/uuid"

	"github.com/gos0001/gomod-cryptopay/internal/domain"
	"github.com/gos0001/gomod-cryptopay/internal/view"
)

type Input struct {
	// Filled from the path, not the body.
	ID uuid.UUID `json:"-"`
}

func (in *Input) Validate() error {
	if in.ID == uuid.Nil {
		return fmt.Errorf("%w: id is required", domain.ErrInvalidInput)
	}
	return nil
}

type Output struct {
	Invoice view.Invoice `json:"invoice"`
}
