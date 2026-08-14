// Package schema_ensure applies the embedded schema before anything else runs.
//
// It is the first bootstrap task on purpose: every other task and every request
// assumes the tables exist.
package schema_ensure

import (
	"context"
	"fmt"

	"go.uber.org/zap"

	"github.com/gos0001/gomod-cryptopay/pkg/dbschema"
)

// Schema is the narrow slice of the applier this use case needs.
type Schema interface {
	Ensure(ctx context.Context) (dbschema.Result, error)
}

type Usecase struct {
	schema Schema
	logger *zap.SugaredLogger
}

// New takes the concrete applier because wire resolves concrete types, not
// interfaces.
func New(applier *dbschema.Applier, logger *zap.SugaredLogger) *Usecase {
	return &Usecase{schema: applier, logger: logger}
}

type Input struct{}

type Output struct {
	Skipped bool
	Reason  string
}

func (uc *Usecase) Execute(ctx context.Context, _ Input) (Output, error) {
	res, err := uc.schema.Ensure(ctx)
	if err != nil {
		return Output{}, fmt.Errorf("ensure schema: %w", err)
	}

	if res.Skipped {
		// Worth a warning rather than an info line: the operator has taken
		// responsibility for the schema, and if they are wrong every query
		// fails at runtime instead of here.
		uc.logger.Warnw("schema apply skipped", "reason", res.Reason)
	} else {
		uc.logger.Infow("schema applied")
	}

	return Output{Skipped: res.Skipped, Reason: res.Reason}, nil
}
