package postgres

import (
	"fmt"
	"math/big"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
)

// The pgtype boundary lives here and nowhere else.
//
// sqlc emits pgtype wrappers for uuid, timestamptz and the nullable scalars
// under sql_package: pgx/v5, and ignores go_type overrides for them. Rather
// than let that spread through every repository method, the whole vocabulary is
// six functions wide and confined to this file.

func ts(t time.Time) pgtype.Timestamptz {
	return pgtype.Timestamptz{Time: t, Valid: true}
}

func tsPtr(t *time.Time) pgtype.Timestamptz {
	if t == nil {
		return pgtype.Timestamptz{}
	}
	return ts(*t)
}

func fromTS(t pgtype.Timestamptz) time.Time {
	if !t.Valid {
		return time.Time{}
	}
	return t.Time
}

func fromTSPtr(t pgtype.Timestamptz) *time.Time {
	if !t.Valid {
		return nil
	}
	out := t.Time
	return &out
}

func uid(id uuid.UUID) pgtype.UUID {
	return pgtype.UUID{Bytes: id, Valid: true}
}

func uidPtr(id *uuid.UUID) pgtype.UUID {
	if id == nil {
		return pgtype.UUID{}
	}
	return uid(*id)
}

func fromUID(id pgtype.UUID) uuid.UUID {
	if !id.Valid {
		return uuid.Nil
	}
	return uuid.UUID(id.Bytes)
}

func fromUIDPtr(id pgtype.UUID) *uuid.UUID {
	if !id.Valid {
		return nil
	}
	out := uuid.UUID(id.Bytes)
	return &out
}

func text(s string) pgtype.Text {
	return pgtype.Text{String: s, Valid: true}
}

// textOrNull maps the empty string to SQL NULL. Used for external_id, where
// "not supplied" must not collide with another caller's "not supplied" in the
// unique index — NULLs do not conflict with each other, empty strings do.
func textOrNull(s string) pgtype.Text {
	if s == "" {
		return pgtype.Text{}
	}
	return text(s)
}

func fromText(t pgtype.Text) string {
	if !t.Valid {
		return ""
	}
	return t.String
}

func int8OrNull(v int64) pgtype.Int8 {
	if v == 0 {
		return pgtype.Int8{}
	}
	return pgtype.Int8{Int64: v, Valid: true}
}

func fromInt8(v pgtype.Int8) int64 {
	if !v.Valid {
		return 0
	}
	return v.Int64
}

// interval builds a pgtype.Interval from a duration. Postgres intervals carry
// months and days as separate fields precisely because those are not fixed
// spans; everything this service measures is a real elapsed duration, so it all
// goes in Microseconds.
func interval(d time.Duration) pgtype.Interval {
	return pgtype.Interval{Microseconds: int64(d / time.Microsecond), Valid: true}
}

// Amounts cross the boundary as base-10 strings, which is what the numeric
// override in sqlc.yaml buys: NUMERIC(78,0) has scale zero, so its text form is
// exactly a big.Int's.

func amount(v *big.Int) string {
	if v == nil {
		return "0"
	}
	return v.String()
}

func fromAmount(s string) (*big.Int, error) {
	v, ok := new(big.Int).SetString(s, 10)
	if !ok {
		// Only reachable if the column stopped being NUMERIC(_, 0) — a scale
		// change would start returning "1.50" here, and silently reading that
		// as zero would be worse than failing.
		return nil, fmt.Errorf("postgres: %q is not an integer amount", s)
	}
	return v, nil
}
