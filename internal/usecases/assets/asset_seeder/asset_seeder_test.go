package asset_seeder

import (
	"context"
	"errors"
	"math/big"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"go.uber.org/zap"

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

const oneTron = `{"assets": [{"network": "tron", "symbol": "USDT",
  "contract_address": "TR7NHqjeKQxGTCi8q8ZY4pL8otSzgjLj6t", "decimals": 6,
  "step": "100", "nonce_max": 500}]}`

func TestLoadConfigReadsAnAsset(t *testing.T) {
	cfg, err := loadFrom(t, oneTron)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(cfg.Assets) != 1 {
		t.Fatalf("got %d assets", len(cfg.Assets))
	}

	a := cfg.Assets[0]
	if a.Network != domain.NetworkTron || a.Symbol != "USDT" || a.Decimals != 6 {
		t.Fatalf("got %+v", a)
	}
	if a.Step.Cmp(big.NewInt(100)) != 0 {
		t.Errorf("step = %s", a.Step)
	}
	if a.NonceMax != 500 {
		t.Errorf("nonce_max = %d", a.NonceMax)
	}
	if !a.Enabled {
		t.Error("a configured asset should be enabled")
	}
}

// The 18-decimal step is the reason step is a string: as a JSON number it would
// arrive through float64 and lose its low digits.
func TestLoadConfigKeepsPrecisionAt18Decimals(t *testing.T) {
	cfg, err := loadFrom(t, `{"assets": [{"network": "bsc", "symbol": "USDT",
      "contract_address": "0x55d398326f99059fF775485246999027B3197955",
      "decimals": 18, "step": "100000000000001"}]}`)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	want, _ := new(big.Int).SetString("100000000000001", 10)
	if got := cfg.Assets[0].Step; got.Cmp(want) != 0 {
		t.Fatalf("step = %s, want %s — the trailing 1 was lost", got, want)
	}
}

// eth_getLogs answers in lowercase, so a checksummed address from the config
// would never compare equal to what the watcher sees.
func TestLoadConfigLowercasesEVMAddresses(t *testing.T) {
	cfg, err := loadFrom(t, `{"assets": [{"network": "bsc", "symbol": "USDT",
      "contract_address": "0x55d398326f99059fF775485246999027B3197955", "decimals": 18}]}`)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	const want = "0x55d398326f99059ff775485246999027b3197955"
	if got := cfg.Assets[0].ContractAddress; got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

// Base58 is case-sensitive: lowercasing a TRON address produces a different
// address entirely.
func TestLoadConfigLeavesTronAddressesAlone(t *testing.T) {
	cfg, err := loadFrom(t, oneTron)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	const want = "TR7NHqjeKQxGTCi8q8ZY4pL8otSzgjLj6t"
	if got := cfg.Assets[0].ContractAddress; got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestLoadConfigDefaultsStepAndNonceMax(t *testing.T) {
	cfg, err := loadFrom(t, `{"assets": [{"network": "tron", "symbol": "USDT",
      "contract_address": "TR7NHqjeKQxGTCi8q8ZY4pL8otSzgjLj6t", "decimals": 6}]}`)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// 0.0001 of a 6-decimal token.
	if got := cfg.Assets[0].Step; got.Cmp(big.NewInt(100)) != 0 {
		t.Errorf("step = %s, want 100", got)
	}
	if got := cfg.Assets[0].NonceMax; got != defaultNonceMax {
		t.Errorf("nonce_max = %d, want %d", got, defaultNonceMax)
	}
}

// A token with fewer decimals than the default step would otherwise ask for a
// negative exponent.
func TestLoadConfigDefaultStepClampsAtLowDecimals(t *testing.T) {
	cfg, err := loadFrom(t, `{"assets": [{"network": "tron", "symbol": "TINY",
      "contract_address": "TR7NHqjeKQxGTCi8q8ZY4pL8otSzgjLj6t", "decimals": 2}]}`)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got := cfg.Assets[0].Step; got.Cmp(big.NewInt(1)) != 0 {
		t.Fatalf("step = %s, want 1", got)
	}
}

func TestLoadConfigRejectsBadEntries(t *testing.T) {
	tests := map[string]struct{ contents, wants string }{
		"no assets at all": {
			`{}`, "at least one token",
		},
		"empty list": {
			`{"assets": []}`, "at least one token",
		},
		"unknown network": {
			`{"assets": [{"network": "solana", "symbol": "USDT",
              "contract_address": "x", "decimals": 6}]}`, "network",
		},
		"empty contract": {
			`{"assets": [{"network": "tron", "symbol": "USDT",
              "contract_address": "", "decimals": 6}]}`, "contract_address",
		},
		"EVM address without 0x": {
			`{"assets": [{"network": "bsc", "symbol": "USDT",
              "contract_address": "55d398326f99059ff775485246999027b3197955", "decimals": 18}]}`, "0x",
		},
		"step is not a number": {
			`{"assets": [{"network": "tron", "symbol": "USDT",
              "contract_address": "TR7N", "decimals": 6, "step": "0.0001"}]}`, "step",
		},
		"negative step": {
			`{"assets": [{"network": "tron", "symbol": "USDT",
              "contract_address": "TR7N", "decimals": 6, "step": "-100"}]}`, "step",
		},
		"empty symbol": {
			`{"assets": [{"network": "tron", "symbol": "",
              "contract_address": "TR7N", "decimals": 6}]}`, "symbol",
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			_, err := loadFrom(t, tc.contents)
			if err == nil {
				t.Fatal("want an error")
			}
			if !strings.Contains(err.Error(), tc.wants) {
				t.Fatalf("error should mention %q: %v", tc.wants, err)
			}
		})
	}
}

// Two entries for one contract would let upsert order decide which wins,
// silently.
func TestLoadConfigRejectsDuplicateContract(t *testing.T) {
	_, err := loadFrom(t, `{"assets": [
      {"network": "tron", "symbol": "USDT", "contract_address": "TR7N", "decimals": 6},
      {"network": "tron", "symbol": "USDT2", "contract_address": "TR7N", "decimals": 6}]}`)
	if err == nil || !strings.Contains(err.Error(), "repeats") {
		t.Fatalf("want a duplicate error, got %v", err)
	}
}

// The same contract on two chains is legitimate and must be allowed.
func TestLoadConfigAllowsSameSymbolOnTwoNetworks(t *testing.T) {
	cfg, err := loadFrom(t, `{"assets": [
      {"network": "tron", "symbol": "USDT", "contract_address": "TR7N", "decimals": 6},
      {"network": "bsc", "symbol": "USDT", "contract_address": "0xabc", "decimals": 18}]}`)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(cfg.Assets) != 2 {
		t.Fatalf("got %d assets", len(cfg.Assets))
	}
}

type fakePostgres struct {
	upserted []domain.Asset
	kept     []int64
	nextID   int64
	upsetErr error
}

func (f *fakePostgres) UpsertAsset(_ context.Context, in domain.Asset) (domain.Asset, error) {
	if f.upsetErr != nil {
		return domain.Asset{}, f.upsetErr
	}
	f.nextID++
	in.ID = f.nextID
	f.upserted = append(f.upserted, in)
	return in, nil
}

func (f *fakePostgres) DisableAssetsNotIn(_ context.Context, keepIDs []int64) (int64, error) {
	f.kept = keepIDs
	return 0, nil
}

func newUsecase(pg Postgres, assets []domain.Asset) *Usecase {
	return &Usecase{postgres: pg, cfg: Config{Assets: assets}, logger: zap.NewNop().Sugar()}
}

func TestExecuteUpsertsEveryAssetAndKeepsTheirIDs(t *testing.T) {
	pg := &fakePostgres{}
	uc := newUsecase(pg, []domain.Asset{
		{Network: domain.NetworkTron, Symbol: "USDT", Step: big.NewInt(100)},
		{Network: domain.NetworkBSC, Symbol: "USDT", Step: big.NewInt(1)},
	})

	out, err := uc.Execute(context.Background(), Input{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if out.Seeded != 2 || len(pg.upserted) != 2 {
		t.Fatalf("seeded %d, upserted %d", out.Seeded, len(pg.upserted))
	}
	if len(pg.kept) != 2 || pg.kept[0] != 1 || pg.kept[1] != 2 {
		t.Fatalf("kept = %v, want the ids storage returned", pg.kept)
	}
}

func TestExecuteReportsUpsertFailure(t *testing.T) {
	sentinel := errors.New("connection refused")
	uc := newUsecase(&fakePostgres{upsetErr: sentinel}, []domain.Asset{
		{Network: domain.NetworkTron, Symbol: "USDT", Step: big.NewInt(100)},
	})

	if _, err := uc.Execute(context.Background(), Input{}); !errors.Is(err, sentinel) {
		t.Fatalf("got %v, want the storage error wrapped", err)
	}
}
