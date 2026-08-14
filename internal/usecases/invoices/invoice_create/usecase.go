// Package invoice_create issues an invoice: it resolves the asset, allocates a
// payment amount nothing else holds, and stores the result.
package invoice_create

import (
	"context"
	"errors"
	"fmt"
	"math/big"
	"strings"
	"time"

	"github.com/google/uuid"

	postgresadapter "github.com/gos0001/gomod-cryptopay/internal/adapter/postgres"
	"github.com/gos0001/gomod-cryptopay/internal/domain"
	"github.com/gos0001/gomod-cryptopay/internal/invoicecfg"
	"github.com/gos0001/gomod-cryptopay/internal/view"
	"github.com/gos0001/gomod-cryptopay/pkg/cryptoamount"
)

// ErrAmbiguousAsset means the symbol matches more than one contract on that
// chain and the caller has to say which.
//
// Declared here rather than in domain because it is a property of this API's
// lookup-by-symbol convenience, not of the payment domain: nothing downstream
// of asset resolution can encounter it.
var ErrAmbiguousAsset = errors.New("symbol matches more than one contract")

// AmbiguousAssetError carries the candidates, so the message can tell the
// caller exactly which contract addresses to choose between instead of making
// them go and read GET /assets.
type AmbiguousAssetError struct {
	Network   domain.Network
	Symbol    string
	Contracts []string
}

func (e *AmbiguousAssetError) Error() string {
	return fmt.Sprintf("%s matches %d contracts on %s (%s); name one in contract_address",
		e.Symbol, len(e.Contracts), e.Network, strings.Join(e.Contracts, ", "))
}

func (e *AmbiguousAssetError) Unwrap() error { return ErrAmbiguousAsset }

type Postgres interface {
	ListAssetsByNetworkAndSymbol(ctx context.Context, network domain.Network, symbol string) ([]domain.Asset, error)
	GetEnabledAssetByContract(ctx context.Context, network domain.Network, contract string) (domain.Asset, error)
	GetAssetByID(ctx context.Context, id int64) (domain.Asset, error)
	CreateInvoice(ctx context.Context, in postgresadapter.CreateInvoiceInput) (domain.Invoice, error)
	GetInvoiceByExternalID(ctx context.Context, externalID string) (domain.Invoice, error)
}

type Usecase struct {
	postgres Postgres
	cfg      invoicecfg.Config
	now      func() time.Time
	newID    func() uuid.UUID
}

// New takes the concrete adapter because wire resolves concrete types.
//
// The clock and the id source are struct fields rather than direct calls so a
// test can pin both; nothing injects them, the constructor sets the real ones.
func New(pg *postgresadapter.Adapter, cfg invoicecfg.Config) *Usecase {
	return &Usecase{postgres: pg, cfg: cfg, now: time.Now, newID: uuid.New}
}

func (uc *Usecase) Execute(ctx context.Context, in Input) (Output, error) {
	network, err := domain.ParseNetwork(in.Network)
	if err != nil {
		return Output{}, err
	}

	asset, err := uc.resolveAsset(ctx, network, in.Symbol, in.ContractAddress)
	if err != nil {
		return Output{}, err
	}

	baseAmount, err := cryptoamount.Parse(in.Amount, asset.Decimals)
	if err != nil {
		// The parser's message names the actual problem — too many fractional
		// digits for this token, say — which is worth far more to the caller
		// than a flat "invalid amount".
		return Output{}, fmt.Errorf("%w: amount: %s", domain.ErrInvalidInput, err)
	}
	if baseAmount.Sign() <= 0 {
		return Output{}, fmt.Errorf("%w: amount must be greater than zero", domain.ErrInvalidInput)
	}

	payAddress := uc.cfg.PayAddress(network)
	if payAddress == "" {
		// Startup validation should have caught this; if configuration changed
		// underneath us, refusing beats issuing an invoice nobody can pay.
		return Output{}, fmt.Errorf("no receiving address configured for %s", network)
	}

	ttl, err := uc.cfg.ResolveTTL(in.expiresIn)
	if err != nil {
		return Output{}, err
	}

	now := uc.now()
	expiresAt := now.Add(ttl)

	invoice, err := uc.postgres.CreateInvoice(ctx, postgresadapter.CreateInvoiceInput{
		ID:          uc.newID(),
		ExternalID:  in.ExternalID,
		Asset:       asset,
		PayAddress:  payAddress,
		BaseAmount:  baseAmount,
		Description: in.Description,
		Metadata:    in.Metadata,
		ExpiresAt:   expiresAt,
		// The hold outlives the invoice: a transfer sent just before expiry can
		// land well after it, and must not pay whoever inherited the amount.
		AmountHoldUntil: expiresAt.Add(uc.cfg.AmountHold.Std()),
	})

	if errors.Is(err, domain.ErrExternalIDTaken) {
		return uc.replay(ctx, in, asset, baseAmount)
	}
	if err != nil {
		return Output{}, err
	}

	return Output{Invoice: view.NewInvoice(invoice, asset), Created: true}, nil
}

// resolveAsset turns the caller's naming of an asset into one row.
func (uc *Usecase) resolveAsset(ctx context.Context, network domain.Network, symbol, contract string) (domain.Asset, error) {
	if contract != "" {
		// EVM addresses are stored lowercase because that is how the chain
		// reports them; a caller sending a checksummed address must still
		// match. TRON is base58 and case-carrying, so it is left alone.
		if network == domain.NetworkBSC {
			contract = strings.ToLower(contract)
		}
		return uc.postgres.GetEnabledAssetByContract(ctx, network, contract)
	}

	assets, err := uc.postgres.ListAssetsByNetworkAndSymbol(ctx, network, symbol)
	if err != nil {
		return domain.Asset{}, err
	}

	switch len(assets) {
	case 0:
		return domain.Asset{}, domain.ErrAssetNotFound
	case 1:
		return assets[0], nil
	default:
		contracts := make([]string, 0, len(assets))
		for _, a := range assets {
			contracts = append(contracts, a.ContractAddress)
		}
		return domain.Asset{}, &AmbiguousAssetError{
			Network: network, Symbol: symbol, Contracts: contracts,
		}
	}
}

// replay answers a repeated external_id.
//
// Returning the existing invoice is the point of the idempotency key: a merchant
// whose first call timed out retries it, and must not end up with two invoices.
// But only when the request agrees with what was stored — handing back an
// invoice for a different amount under the same key would hide a real bug on
// their side until reconciliation.
func (uc *Usecase) replay(ctx context.Context, in Input, asset domain.Asset, baseAmount *big.Int) (Output, error) {
	existing, err := uc.postgres.GetInvoiceByExternalID(ctx, in.ExternalID)
	if err != nil {
		if errors.Is(err, domain.ErrInvoiceNotFound) {
			// The row was there a moment ago. Either it has just been deleted,
			// or the unique violation came from somewhere else entirely; either
			// way the original error is the honest answer.
			return Output{}, domain.ErrExternalIDTaken
		}
		return Output{}, err
	}

	if existing.AssetID != asset.ID || existing.BaseAmount.Cmp(baseAmount) != 0 {
		return Output{}, domain.ErrExternalIDTaken
	}

	// The stored invoice may predate a configuration change, so its asset is
	// re-read rather than assumed to be the one just resolved.
	storedAsset, err := uc.postgres.GetAssetByID(ctx, existing.AssetID)
	if err != nil {
		return Output{}, err
	}

	return Output{Invoice: view.NewInvoice(existing, storedAsset), Created: false}, nil
}
