package asset_seeder

import (
	"errors"
	"fmt"
	"math/big"
	"strings"

	"github.com/gos0001/gomod-cryptopay/internal/domain"
	"github.com/gos0001/gomod-cryptopay/pkg/config"
)

// defaultNonceMax caps how many invoices can be outstanding for one base amount.
// A thousand is deep enough that exhausting it means a genuine burst rather than
// a misconfiguration, and shallow enough that the largest possible surcharge
// stays under a tenth of a step-unit.
const defaultNonceMax = 1000

// defaultStepDecimals is how far above the token's smallest unit the default
// step sits: 0.0001 of a token. Small enough that the surcharge is invisible on
// a payment of any size, large enough that a wallet rounding to four decimal
// places still lands in the right window.
const defaultStepDecimals = 4

// Entry is one asset as written in the configuration file.
//
// Only network, symbol, contract_address and decimals are required. step and
// nonce_max exist for operators who need to tune the matching grid, and default
// to values that suit a stablecoin.
type Entry struct {
	Network         string `json:"network"`
	Symbol          string `json:"symbol"`
	ContractAddress string `json:"contract_address"`
	Decimals        int32  `json:"decimals"`

	// Step is a decimal string of smallest units, not a number: at 18 decimals
	// the value exceeds what JSON's float64 can hold exactly. Same reason every
	// amount in this service is a big.Int.
	Step     string `json:"step"`
	NonceMax int32  `json:"nonce_max"`
}

type Config struct {
	Assets []domain.Asset
}

// LoadConfig reads the assets section and turns it into validated domain assets.
//
// Everything is checked here rather than at seeding time, so a malformed asset
// stops the process at startup with a message naming the entry — instead of
// surfacing later as a 400 on every invoice for that token.
func LoadConfig(f *config.File) (Config, error) {
	var entries []Entry
	if err := f.Section("assets", &entries); err != nil {
		return Config{}, err
	}

	if len(entries) == 0 {
		return Config{}, errors.New("config: assets is required and must list at least one token; " +
			"a service with no assets cannot create any invoice")
	}

	assets := make([]domain.Asset, 0, len(entries))
	seen := make(map[string]int, len(entries))

	for i, e := range entries {
		asset, err := e.toDomain()
		if err != nil {
			return Config{}, fmt.Errorf("config: assets[%d]: %w", i, err)
		}

		// The pair is the real key in storage, so a duplicate here would make
		// the upsert order decide which entry wins — silently.
		key := string(asset.Network) + "|" + asset.ContractAddress
		if first, dup := seen[key]; dup {
			return Config{}, fmt.Errorf("config: assets[%d] repeats the contract in assets[%d] "+
				"(%s on %s)", i, first, asset.ContractAddress, asset.Network)
		}
		seen[key] = i

		assets = append(assets, asset)
	}

	return Config{Assets: assets}, nil
}

func (e Entry) toDomain() (domain.Asset, error) {
	network, err := domain.ParseNetwork(e.Network)
	if err != nil {
		return domain.Asset{}, fmt.Errorf("network %q: %w", e.Network, err)
	}

	contract, err := normaliseAddress(network, e.ContractAddress)
	if err != nil {
		return domain.Asset{}, err
	}

	step, err := resolveStep(e.Step, e.Decimals)
	if err != nil {
		return domain.Asset{}, err
	}

	nonceMax := e.NonceMax
	if nonceMax == 0 {
		nonceMax = defaultNonceMax
	}

	asset := domain.Asset{
		Network:         network,
		Symbol:          strings.TrimSpace(e.Symbol),
		ContractAddress: contract,
		Decimals:        e.Decimals,
		Step:            step,
		NonceMax:        nonceMax,
		Enabled:         true,
	}
	if err := asset.Validate(); err != nil {
		return domain.Asset{}, err
	}
	return asset, nil
}

// normaliseAddress puts an address into the form the watcher will see it in.
//
// EVM addresses are case-insensitive hex, and eth_getLogs answers in lowercase,
// so a checksummed address from the configuration would never compare equal.
// TRON addresses are base58, where case carries meaning — lowercasing one
// produces a different address. Hence the switch rather than a blanket
// strings.ToLower.
func normaliseAddress(network domain.Network, addr string) (string, error) {
	addr = strings.TrimSpace(addr)
	if addr == "" {
		return "", errors.New("contract_address is empty")
	}

	switch network {
	case domain.NetworkBSC:
		if !strings.HasPrefix(addr, "0x") && !strings.HasPrefix(addr, "0X") {
			return "", fmt.Errorf("contract_address %q is not an EVM address (expected a 0x prefix)", addr)
		}
		return strings.ToLower(addr), nil
	default:
		return addr, nil
	}
}

// resolveStep parses the configured step, or derives one from the token's scale.
func resolveStep(raw string, decimals int32) (*big.Int, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		exp := decimals - defaultStepDecimals
		if exp < 0 {
			exp = 0
		}
		return new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(exp)), nil), nil
	}

	step, ok := new(big.Int).SetString(raw, 10)
	if !ok {
		return nil, fmt.Errorf("step %q is not a whole number of smallest units", raw)
	}
	return step, nil
}
