package domain

import "errors"

// Network is a chain this service watches.
//
// The set is closed because each member needs its own watcher implementation —
// unlike assets, which are configuration. Adding one is a code change by
// definition, so an open string type would only hide the fact.
type Network string

const (
	NetworkTron Network = "tron"
	NetworkBSC  Network = "bsc"
)

// ErrUnknownNetwork is returned for a network string this build cannot watch.
var ErrUnknownNetwork = errors.New("unknown network")

func (n Network) Valid() bool {
	switch n {
	case NetworkTron, NetworkBSC:
		return true
	default:
		return false
	}
}

func (n Network) String() string { return string(n) }

// Networks lists every supported network, in a stable order.
func Networks() []Network { return []Network{NetworkTron, NetworkBSC} }

// ParseNetwork accepts the canonical name only. Aliases ("trc20", "bep20",
// "binance-smart-chain") are deliberately not accepted: they name a token
// standard or a former brand, not a chain, and letting both in means two
// spellings of the same row in cp_chain_cursors.
func ParseNetwork(s string) (Network, error) {
	n := Network(s)
	if !n.Valid() {
		return "", ErrUnknownNetwork
	}
	return n, nil
}
