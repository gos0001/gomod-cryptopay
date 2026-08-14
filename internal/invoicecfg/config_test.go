package invoicecfg

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gos0001/gomod-cryptopay/internal/domain"
	"github.com/gos0001/gomod-cryptopay/pkg/config"
)

func loadFrom(t *testing.T, contents string) (Config, error) {
	t.Helper()

	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}

	file, err := config.Load(config.Path(path))
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	return LoadConfig(file)
}

const tronOnly = `{
  "invoices": {"pay_address_tron": "TR7NHqjeKQxGTCi8q8ZY4pL8otSzgjLj6t"},
  "assets": [{"network": "tron", "symbol": "USDT", "contract_address": "TR7N", "decimals": 6}]
}`

func TestLoadConfigDefaults(t *testing.T) {
	cfg, err := loadFrom(t, tronOnly)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if cfg.TTL.Std() != 30*time.Minute {
		t.Errorf("ttl = %s", cfg.TTL)
	}
	if cfg.AmountHold.Std() != 2*time.Hour {
		t.Errorf("amount_hold = %s", cfg.AmountHold)
	}
	if cfg.ExpireInterval.Std() != time.Minute {
		t.Errorf("expire_interval = %s", cfg.ExpireInterval)
	}
}

// A deployment that only takes TRON must not be forced to invent a BSC address.
func TestLoadConfigOnlyRequiresAddressesForConfiguredNetworks(t *testing.T) {
	cfg, err := loadFrom(t, tronOnly)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.PayAddressBSC != "" {
		t.Error("no BSC address should be needed")
	}
}

// The check that earns this package's cross-section read: an invoice issued
// with no receiving address is useless, and the merchant's first payment is the
// worst place to discover it.
func TestLoadConfigRequiresAddressForEachConfiguredNetwork(t *testing.T) {
	_, err := loadFrom(t, `{
      "invoices": {"pay_address_tron": "T..."},
      "assets": [
        {"network": "tron", "symbol": "USDT", "contract_address": "TR7N", "decimals": 6},
        {"network": "bsc",  "symbol": "USDT", "contract_address": "0xabc", "decimals": 18}
      ]
    }`)

	if err == nil {
		t.Fatal("want an error")
	}
	if !strings.Contains(err.Error(), "pay_address_bsc") {
		t.Fatalf("error should name the missing address: %v", err)
	}
}

func TestLoadConfigRejectsBlankAddress(t *testing.T) {
	_, err := loadFrom(t, `{
      "invoices": {"pay_address_tron": "   "},
      "assets": [{"network": "tron", "symbol": "USDT", "contract_address": "TR7N", "decimals": 6}]
    }`)
	if err == nil {
		t.Fatal("whitespace is not an address")
	}
}

func TestLoadConfigRejectsBadDurations(t *testing.T) {
	tests := map[string]string{
		"zero ttl":         `"ttl": "0s"`,
		"ttl above cap":    `"ttl": "48h"`,
		"zero amount_hold": `"amount_hold": "0s"`,
	}

	for name, field := range tests {
		t.Run(name, func(t *testing.T) {
			_, err := loadFrom(t, `{
              "invoices": {"pay_address_tron": "T...", `+field+`},
              "assets": [{"network": "tron", "symbol": "USDT", "contract_address": "TR7N", "decimals": 6}]
            }`)
			if err == nil {
				t.Fatal("want an error")
			}
		})
	}
}

// Zero is the documented off switch for the expirer, so it must load.
func TestLoadConfigAllowsZeroExpireInterval(t *testing.T) {
	cfg, err := loadFrom(t, `{
      "invoices": {"pay_address_tron": "T...", "expire_interval": "0s"},
      "assets": [{"network": "tron", "symbol": "USDT", "contract_address": "TR7N", "decimals": 6}]
    }`)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.ExpireInterval.Std() != 0 {
		t.Fatalf("got %s", cfg.ExpireInterval)
	}
}

func TestPayAddress(t *testing.T) {
	cfg := Config{PayAddressTron: "T1", PayAddressBSC: "0x1"}

	if got := cfg.PayAddress(domain.NetworkTron); got != "T1" {
		t.Errorf("tron = %q", got)
	}
	if got := cfg.PayAddress(domain.NetworkBSC); got != "0x1" {
		t.Errorf("bsc = %q", got)
	}
	if got := cfg.PayAddress(domain.Network("solana")); got != "" {
		t.Errorf("unknown network = %q, want empty", got)
	}
}

func TestResolveTTL(t *testing.T) {
	cfg := Config{TTL: config.Duration(30 * time.Minute)}

	got, err := cfg.ResolveTTL(0)
	if err != nil || got != 30*time.Minute {
		t.Fatalf("no request should fall back to the default: %s, %v", got, err)
	}

	got, err = cfg.ResolveTTL(90 * time.Minute)
	if err != nil || got != 90*time.Minute {
		t.Fatalf("a request above the default is fine: %s, %v", got, err)
	}

	if _, err := cfg.ResolveTTL(-time.Minute); !errors.Is(err, domain.ErrInvalidInput) {
		t.Fatalf("negative: got %v", err)
	}
	if _, err := cfg.ResolveTTL(maxTTL + time.Second); !errors.Is(err, domain.ErrInvalidInput) {
		t.Fatalf("above the cap: got %v", err)
	}
}
