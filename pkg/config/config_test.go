package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// write puts contents in a temp file and returns its path.
func write(t *testing.T, contents string) Path {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	return Path(path)
}

func load(t *testing.T, contents string) *File {
	t.Helper()
	f, err := Load(write(t, contents))
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	return f
}

type pgSection struct {
	DSN        string `json:"dsn"`
	AutoCreate bool   `json:"auto_create"`
}

func TestSectionDecodes(t *testing.T) {
	f := load(t, `{"postgres": {"dsn": "postgres://localhost/db", "auto_create": false}}`)

	cfg := pgSection{AutoCreate: true}
	if err := f.Section("postgres", &cfg); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if cfg.DSN != "postgres://localhost/db" {
		t.Errorf("dsn = %q", cfg.DSN)
	}
	if cfg.AutoCreate {
		t.Error("auto_create should have been overridden to false")
	}
}

// A default must survive a key the file does not mention. This is the whole
// reason defaults are plain Go values set before the call.
func TestSectionLeavesDefaultsAlone(t *testing.T) {
	f := load(t, `{"postgres": {"dsn": "x"}}`)

	cfg := pgSection{AutoCreate: true}
	if err := f.Section("postgres", &cfg); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !cfg.AutoCreate {
		t.Error("auto_create default was clobbered by an absent key")
	}
}

// An absent section is not an error: whether a missing value is acceptable is
// the consuming package's decision.
func TestSectionAbsentIsNotAnError(t *testing.T) {
	f := load(t, `{}`)

	cfg := pgSection{AutoCreate: true}
	if err := f.Section("postgres", &cfg); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !cfg.AutoCreate || cfg.DSN != "" {
		t.Fatalf("got %+v, want untouched defaults", cfg)
	}
}

func TestSectionReportsWrongType(t *testing.T) {
	f := load(t, `{"postgres": {"dsn": 42}}`)

	var cfg pgSection
	err := f.Section("postgres", &cfg)
	if err == nil {
		t.Fatal("want an error for a number where a string belongs")
	}
	if !strings.Contains(err.Error(), "postgres") {
		t.Errorf("error should name the section: %v", err)
	}
}

func TestSectionDecodesArray(t *testing.T) {
	f := load(t, `{"assets": [{"symbol": "USDT"}, {"symbol": "USDC"}]}`)

	var assets []struct {
		Symbol string `json:"symbol"`
	}
	if err := f.Section("assets", &assets); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(assets) != 2 || assets[0].Symbol != "USDT" || assets[1].Symbol != "USDC" {
		t.Fatalf("got %+v", assets)
	}
}

func TestLoadRejectsMissingFile(t *testing.T) {
	_, err := Load(Path(filepath.Join(t.TempDir(), "absent.json")))
	if err == nil {
		t.Fatal("want an error")
	}
	if !strings.Contains(err.Error(), "-config") {
		t.Errorf("error should say how to point elsewhere: %v", err)
	}
}

// "invalid character '}'" is not a location in a hundred-line file.
func TestLoadReportsSyntaxPosition(t *testing.T) {
	_, err := Load(write(t, "{\n  \"app\": {\n    \"addr\": \n  }\n}\n"))
	if err == nil {
		t.Fatal("want an error")
	}
	if !strings.Contains(err.Error(), "line ") {
		t.Errorf("error should carry a line number: %v", err)
	}
}

func TestWarningsFlagsUnknownKey(t *testing.T) {
	f := load(t, `{"postgres": {"dsn": "x", "dsnn": "typo"}}`)

	var cfg pgSection
	if err := f.Section("postgres", &cfg); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	warnings := f.Warnings()
	if len(warnings) != 1 || !strings.Contains(warnings[0], "dsnn") {
		t.Fatalf("got %v, want one warning naming dsnn", warnings)
	}
}

func TestWarningsFlagsUnknownSection(t *testing.T) {
	f := load(t, `{"postgres": {"dsn": "x"}, "postgrez": {}}`)

	var cfg pgSection
	if err := f.Section("postgres", &cfg); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	warnings := f.Warnings()
	if len(warnings) != 1 || !strings.Contains(warnings[0], "postgrez") {
		t.Fatalf("got %v, want one warning naming postgrez", warnings)
	}
}

// The case the accumulating claim set exists for: pkg/postgres and pkg/dbschema
// both read the postgres section, and neither may declare the other's field
// unknown.
func TestWarningsAllowsTwoConsumersOfOneSection(t *testing.T) {
	f := load(t, `{"postgres": {"dsn": "x", "auto_create": true, "auto_schema": false}}`)

	var pg pgSection
	if err := f.Section("postgres", &pg); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var schema struct {
		AutoSchema bool `json:"auto_schema"`
	}
	if err := f.Section("postgres", &schema); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if warnings := f.Warnings(); len(warnings) != 0 {
		t.Fatalf("got %v, want none", warnings)
	}
}

// Order must not matter: the second consumer's claim has to count even when
// Warnings would otherwise have seen an incomplete set.
func TestWarningsIsOrderIndependent(t *testing.T) {
	const contents = `{"postgres": {"dsn": "x", "auto_schema": false}}`

	f := load(t, contents)
	var schema struct {
		AutoSchema bool `json:"auto_schema"`
	}
	if err := f.Section("postgres", &schema); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var pg pgSection
	if err := f.Section("postgres", &pg); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if warnings := f.Warnings(); len(warnings) != 0 {
		t.Fatalf("got %v, want none", warnings)
	}
}

// An array section has no top-level keys to misspell, and its element fields
// belong to whoever consumes it.
func TestWarningsSkipsArraySections(t *testing.T) {
	f := load(t, `{"assets": [{"symbol": "USDT", "whatever": 1}]}`)

	var assets []struct {
		Symbol string `json:"symbol"`
	}
	if err := f.Section("assets", &assets); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if warnings := f.Warnings(); len(warnings) != 0 {
		t.Fatalf("got %v, want none", warnings)
	}
}

func TestWarningsIgnoresUntaggedAndSkippedFields(t *testing.T) {
	f := load(t, `{"thing": {"Exported": 1, "renamed": 2}}`)

	var cfg struct {
		Exported int `json:"-"`
		Renamed  int `json:"renamed"`
		Untagged string
	}
	if err := f.Section("thing", &cfg); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Exported is json:"-" so the key is genuinely unknown; renamed is claimed.
	warnings := f.Warnings()
	if len(warnings) != 1 || !strings.Contains(warnings[0], "Exported") {
		t.Fatalf("got %v, want one warning naming Exported", warnings)
	}
}

func TestDuration(t *testing.T) {
	f := load(t, `{"cron": {"shutdown_timeout": "90s", "lease": "2h30m"}}`)

	var cfg struct {
		ShutdownTimeout Duration `json:"shutdown_timeout"`
		Lease           Duration `json:"lease"`
		Absent          Duration `json:"absent"`
	}
	cfg.Absent = Duration(5 * time.Second)

	if err := f.Section("cron", &cfg); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if cfg.ShutdownTimeout.Std() != 90*time.Second {
		t.Errorf("shutdown_timeout = %s", cfg.ShutdownTimeout)
	}
	if cfg.Lease.Std() != 150*time.Minute {
		t.Errorf("lease = %s", cfg.Lease)
	}
	if cfg.Absent.Std() != 5*time.Second {
		t.Errorf("absent default clobbered: %s", cfg.Absent)
	}
}

func TestDurationRejectsBadInput(t *testing.T) {
	tests := map[string]string{
		"bare number":  `{"x": {"d": 60}}`,
		"no unit":      `{"x": {"d": "60"}}`,
		"not a period": `{"x": {"d": "soon"}}`,
	}

	for name, contents := range tests {
		t.Run(name, func(t *testing.T) {
			f := load(t, contents)
			var cfg struct {
				D Duration `json:"d"`
			}
			if err := f.Section("x", &cfg); err == nil {
				t.Fatalf("want an error, got %s", cfg.D)
			}
		})
	}
}

// A configuration this service prints must be readable back in.
func TestDurationRoundTrips(t *testing.T) {
	original := Duration(90 * time.Second)

	encoded, err := original.MarshalJSON()
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var back Duration
	if err := back.UnmarshalJSON(encoded); err != nil {
		t.Fatalf("unmarshal %s: %v", encoded, err)
	}
	if back != original {
		t.Fatalf("got %s, want %s", back, original)
	}
}
