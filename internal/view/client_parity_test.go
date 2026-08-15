package view

import (
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/gos0001/gomod-cryptopay/internal/usecases/sys/sys_health"
	"github.com/gos0001/gomod-cryptopay/pkg/cryptopay"
)

// The client cannot import this package — Go forbids internal/ from outside the
// module — so pkg/cryptopay declares its own copy of every response shape. The
// two will drift apart the first time a field is added here and forgotten there,
// and the drift is silent: the client simply never sees the new field.
//
// The check lives on this side because internal/ may import pkg/, and the
// project contract forbids the reverse.

func TestClientTypesMatchTheAPIShapes(t *testing.T) {
	pairs := []struct {
		name   string
		server any
		client any
	}{
		{"Invoice", Invoice{}, cryptopay.Invoice{}},
		{"Asset", Asset{}, cryptopay.Asset{}},
		{"Orphan", Orphan{}, cryptopay.Orphan{}},
		{"Health", sys_health.Output{}, cryptopay.Health{}},
	}

	for _, pair := range pairs {
		t.Run(pair.name, func(t *testing.T) {
			server := jsonFields(pair.server)
			client := jsonFields(pair.client)

			for _, field := range server {
				if !contains(client, field) {
					t.Errorf("%s: the API sends %q and pkg/cryptopay.%s has no field for it; "+
						"add it to the client, or a consumer silently never sees it",
						pair.name, field, pair.name)
				}
			}
			for _, field := range client {
				if !contains(server, field) {
					t.Errorf("%s: pkg/cryptopay.%s declares %q, which the API does not send; "+
						"it will always be the zero value",
						pair.name, pair.name, field)
				}
			}
		})
	}
}

// Types the client parses out of a webhook payload rather than out of a
// response. The payload is built by hand in matching.enqueue, so this compares
// against that map's keys, kept here as the one place both sides are visible.
func TestClientEventMatchesTheWebhookPayload(t *testing.T) {
	// Mirrors the json.Marshal in internal/service/matching.enqueue.
	sent := []string{"event", "invoice_id", "status", "network", "symbol", "pay_amount", "decimals"}

	got := jsonFields(cryptopay.Event{})

	for _, field := range sent {
		if !contains(got, field) {
			t.Errorf("the webhook payload carries %q and cryptopay.Event has no field for it", field)
		}
	}
	for _, field := range got {
		if !contains(sent, field) {
			t.Errorf("cryptopay.Event declares %q, which the payload does not carry", field)
		}
	}
}

// jsonFields returns the JSON names of every tagged, exported field. Fields
// without a tag are ignored: they are the client's own additions, such as the
// raw body, which never appear on the wire.
func jsonFields(v any) []string {
	typ := reflect.TypeOf(v)

	var names []string
	for i := range typ.NumField() {
		field := typ.Field(i)
		if !field.IsExported() {
			continue
		}

		tag, ok := field.Tag.Lookup("json")
		if !ok {
			continue
		}
		name, _, _ := strings.Cut(tag, ",")
		if name == "" || name == "-" {
			continue
		}
		names = append(names, name)
	}

	sort.Strings(names)
	return names
}

func contains(haystack []string, needle string) bool {
	for _, s := range haystack {
		if s == needle {
			return true
		}
	}
	return false
}
