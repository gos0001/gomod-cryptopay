// Package config reads the service's JSON configuration file and hands each
// package its own section.
//
// There is no environment-variable fallback. One file is the whole surface, so
// there is exactly one place to look when a value is not what you expected, and
// no invisible override to rule out first. The cost is that the file carries
// secrets, and therefore has to be mounted into the container rather than baked
// into the image.
//
// This package deliberately does not know the shape of the configuration. It
// holds no Root struct enumerating every section — each package keeps its own
// Config in its own config.go and asks for its section by name, which is the
// same ownership the service had under envconfig. All that changed is where the
// values come from.
//
// Zero domain imports.
package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"reflect"
	"sort"
	"strings"
	"sync"
)

// Path is the configuration file's location.
//
// A named type rather than a bare string so wire can resolve it: the graph
// contains other strings, and wire matches providers by type alone.
type Path string

// File is a parsed configuration file.
type File struct {
	path     string
	sections map[string]json.RawMessage

	// mu guards claimed. Sections are consumed while wire builds the graph,
	// which is single-threaded, but nothing in the type enforces that and a
	// mutex is cheaper than the bug.
	mu sync.Mutex
	// claimed records which field names some package has asked for, per
	// section. It accumulates across calls because one section can have more
	// than one consumer — pkg/postgres reads postgres.dsn while pkg/dbschema
	// reads postgres.auto_schema, and neither may declare the other's field
	// unknown.
	claimed map[string]map[string]bool
}

// Load reads and parses the file.
//
// A missing file is an error rather than a signal to run on defaults. Neither
// the database URL nor the API keys have a sensible default, and a service that
// started without them would be one with no storage and an open API.
func Load(path Path) (*File, error) {
	data, err := os.ReadFile(string(path))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("config: no configuration file at %q "+
				"(pass -config to point somewhere else)", path)
		}
		return nil, fmt.Errorf("config: read %q: %w", path, err)
	}

	var sections map[string]json.RawMessage
	if err := json.Unmarshal(data, &sections); err != nil {
		return nil, fmt.Errorf("config: %q is not valid JSON: %w", path, describe(data, err))
	}

	return &File{
		path:     string(path),
		sections: sections,
		claimed:  make(map[string]map[string]bool),
	}, nil
}

// Path returns where the configuration was read from, for log lines and errors.
func (f *File) Path() string { return f.path }

// Section decodes one top-level section into dst.
//
// dst arrives with its defaults already set as ordinary Go values, and a section
// that is absent from the file leaves them untouched. Whether a missing value is
// acceptable is the calling package's decision, not this one's — Section reports
// only malformed input, never absence.
func (f *File) Section(name string, dst any) error {
	f.claim(name, fieldNames(dst))

	raw, ok := f.sections[name]
	if !ok {
		return nil
	}

	if err := json.Unmarshal(raw, dst); err != nil {
		return fmt.Errorf("config: section %q in %s: %w", name, f.path, err)
	}
	return nil
}

// Warnings lists configuration the service ignored: sections nothing asked for,
// and keys inside a section that no consumer of that section declared.
//
// Warnings rather than errors. A key that a newer build understands must not
// stop an older one from starting during a rollback — but a typo silently
// applying a default is exactly how a payment service ends up watching the wrong
// address, so it has to be said out loud.
//
// Only meaningful once every package has loaded its section, which is to say
// after the wire graph has been built.
func (f *File) Warnings() []string {
	f.mu.Lock()
	defer f.mu.Unlock()

	var out []string

	for name, raw := range f.sections {
		known, consumed := f.claimed[name]
		if !consumed {
			out = append(out, fmt.Sprintf("unknown configuration section %q", name))
			continue
		}

		// Only objects have keys to check. An array section — assets — is
		// validated by whoever consumes it; there is no top-level key here to
		// misspell.
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(raw, &fields); err != nil {
			continue
		}

		for key := range fields {
			if !known[key] {
				out = append(out, fmt.Sprintf("unknown configuration key %q in section %q", key, name))
			}
		}
	}

	sort.Strings(out)
	return out
}

func (f *File) claim(section string, fields []string) {
	f.mu.Lock()
	defer f.mu.Unlock()

	known, ok := f.claimed[section]
	if !ok {
		known = make(map[string]bool, len(fields))
		f.claimed[section] = known
	}
	for _, name := range fields {
		known[name] = true
	}
}

// fieldNames returns the JSON names of dst's top-level fields.
//
// Only the top level: a nested object inside a section belongs to whoever
// declared it, and this package has no business validating that far in. A
// non-struct dst — a slice, for the assets section — contributes no names,
// which is what makes Warnings skip key checking for it.
func fieldNames(dst any) []string {
	v := reflect.ValueOf(dst)
	for v.Kind() == reflect.Pointer {
		if v.IsNil() {
			return nil
		}
		v = v.Elem()
	}
	if v.Kind() != reflect.Struct {
		return nil
	}

	t := v.Type()
	names := make([]string, 0, t.NumField())
	for i := 0; i < t.NumField(); i++ {
		field := t.Field(i)
		if !field.IsExported() {
			continue
		}

		name, _, _ := strings.Cut(field.Tag.Get("json"), ",")
		switch name {
		case "-":
			continue
		case "":
			name = field.Name
		}
		names = append(names, name)
	}
	return names
}

// describe turns a JSON syntax error's byte offset into a line and column.
// Without it the message is "invalid character '}'", which in a hundred-line
// configuration file is not a location.
func describe(data []byte, err error) error {
	var offset int64
	switch e := err.(type) {
	case *json.SyntaxError:
		offset = e.Offset
	case *json.UnmarshalTypeError:
		offset = e.Offset
	default:
		return err
	}

	line, col := 1, 1
	for i := int64(0); i < offset && i < int64(len(data)); i++ {
		if data[i] == '\n' {
			line++
			col = 1
			continue
		}
		col++
	}
	return fmt.Errorf("%w (line %d, column %d)", err, line, col)
}
