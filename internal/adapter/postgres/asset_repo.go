package postgres

import (
	"context"
	"fmt"

	"github.com/gos0001/gomod-cryptopay/internal/adapter/postgres/generated"
	"github.com/gos0001/gomod-cryptopay/internal/domain"
)

func toDomainAsset(row generated.CpAsset) (domain.Asset, error) {
	step, err := fromAmount(row.Step)
	if err != nil {
		return domain.Asset{}, err
	}
	return domain.Asset{
		ID:              row.ID,
		Network:         domain.Network(row.Network),
		Symbol:          row.Symbol,
		ContractAddress: row.ContractAddress,
		Decimals:        int32(row.Decimals),
		Step:            step,
		NonceMax:        row.NonceMax,
		Enabled:         row.Enabled,
	}, nil
}

func toDomainAssets(rows []generated.CpAsset) ([]domain.Asset, error) {
	out := make([]domain.Asset, 0, len(rows))
	for _, row := range rows {
		a, err := toDomainAsset(row)
		if err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, nil
}

// UpsertAsset writes one configured asset, returning the stored row.
func (a *Adapter) UpsertAsset(ctx context.Context, in domain.Asset) (domain.Asset, error) {
	row, err := a.q.UpsertAsset(ctx, generated.UpsertAssetParams{
		Network:         string(in.Network),
		Symbol:          in.Symbol,
		ContractAddress: in.ContractAddress,
		Decimals:        int16(in.Decimals),
		Step:            amount(in.Step),
		NonceMax:        in.NonceMax,
	})
	if err != nil {
		return domain.Asset{}, MapError(err, domain.ErrAssetNotFound)
	}
	return toDomainAsset(row)
}

// DisableAssetsNotIn switches off every enabled asset outside keepIDs.
//
// Disabling rather than deleting: invoices reference these rows, and their
// history has to stay readable after an operator drops a token from the config.
func (a *Adapter) DisableAssetsNotIn(ctx context.Context, keepIDs []int64) (int64, error) {
	n, err := a.q.DisableAssetsNotIn(ctx, keepIDs)
	if err != nil {
		return 0, MapError(err, nil)
	}
	return n, nil
}

func (a *Adapter) GetAssetByID(ctx context.Context, id int64) (domain.Asset, error) {
	row, err := a.q.GetAssetByID(ctx, id)
	if err != nil {
		return domain.Asset{}, MapError(err, domain.ErrAssetNotFound)
	}
	return toDomainAsset(row)
}

// ListAssetsByNetworkAndSymbol returns every enabled asset matching the pair.
//
// A slice rather than one row because a symbol is a label, not a key: two
// contracts under one symbol on one chain are legitimate, and picking one for
// the caller would issue an invoice in a token they did not ask for.
func (a *Adapter) ListAssetsByNetworkAndSymbol(ctx context.Context, network domain.Network, symbol string) ([]domain.Asset, error) {
	rows, err := a.q.ListAssetsByNetworkAndSymbol(ctx, generated.ListAssetsByNetworkAndSymbolParams{
		Network: string(network),
		Symbol:  symbol,
	})
	if err != nil {
		return nil, MapError(err, nil)
	}
	return toDomainAssets(rows)
}

// GetEnabledAssetByContract is the unambiguous lookup used when a caller names
// a contract address.
func (a *Adapter) GetEnabledAssetByContract(ctx context.Context, network domain.Network, contract string) (domain.Asset, error) {
	row, err := a.q.GetEnabledAssetByContract(ctx, generated.GetEnabledAssetByContractParams{
		Network:         string(network),
		ContractAddress: contract,
	})
	if err != nil {
		return domain.Asset{}, MapError(err, domain.ErrAssetNotFound)
	}
	return toDomainAsset(row)
}

// GetAssetByContract includes disabled assets: the watcher still has to
// recognise a transfer of a token that was switched off after the invoice for
// it was issued.
func (a *Adapter) GetAssetByContract(ctx context.Context, network domain.Network, contract string) (domain.Asset, error) {
	row, err := a.q.GetAssetByContract(ctx, generated.GetAssetByContractParams{
		Network:         string(network),
		ContractAddress: contract,
	})
	if err != nil {
		return domain.Asset{}, MapError(err, domain.ErrAssetNotFound)
	}
	return toDomainAsset(row)
}

// ListAssets returns configured assets; enabledOnly hides disabled ones.
func (a *Adapter) ListAssets(ctx context.Context, enabledOnly bool) ([]domain.Asset, error) {
	rows, err := a.q.ListAssets(ctx, enabledOnly)
	if err != nil {
		return nil, MapError(err, nil)
	}
	return toDomainAssets(rows)
}

// ListEnabledAssetsByNetwork is what a watcher asks for on every poll: the
// contracts it must filter on.
func (a *Adapter) ListEnabledAssetsByNetwork(ctx context.Context, network domain.Network) ([]domain.Asset, error) {
	rows, err := a.q.ListEnabledAssetsByNetwork(ctx, string(network))
	if err != nil {
		return nil, fmt.Errorf("list assets for %s: %w", network, MapError(err, nil))
	}
	return toDomainAssets(rows)
}
