package config

import (
	"encoding/json"
	"fmt"
	"time"
)

// Duration is a time.Duration written as a string in JSON: "60s", "30m", "2h".
//
// JSON has one number type and no unit, so a bare 60 would mean nanoseconds to
// time.Duration and seconds to whoever wrote it. The string form is unambiguous
// and is what Centrifugo settled on for the same reason.
type Duration time.Duration

func (d Duration) Std() time.Duration { return time.Duration(d) }

func (d Duration) String() string { return time.Duration(d).String() }

func (d *Duration) UnmarshalJSON(b []byte) error {
	var s string
	if err := json.Unmarshal(b, &s); err != nil {
		return fmt.Errorf("duration must be a string such as \"30s\": %w", err)
	}

	parsed, err := time.ParseDuration(s)
	if err != nil {
		return fmt.Errorf("%q is not a duration (expected a number with a unit, "+
			"such as \"500ms\", \"30s\", \"2h\")", s)
	}

	*d = Duration(parsed)
	return nil
}

// MarshalJSON keeps the string form on the way out, so a configuration this
// service prints can be fed straight back in.
func (d Duration) MarshalJSON() ([]byte, error) {
	return json.Marshal(time.Duration(d).String())
}
