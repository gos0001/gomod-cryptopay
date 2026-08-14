package bsc_watcher

import (
	"fmt"
	"math/big"
	"strings"
)

// valueFromData reads the transfer amount out of a log's data field.
//
// `value` is the one unindexed parameter of an ERC20 Transfer, so it is the
// whole of data: a single uint256, 32 bytes, hex-encoded. Parsed as a big.Int
// because at 18 decimals the value does not fit anything narrower — an amount
// like 7250000000000000001 loses its last digit through a float64 and overflows
// an int64 an order of magnitude above.
func valueFromData(data string) (*big.Int, error) {
	hex := strings.TrimPrefix(strings.TrimPrefix(strings.TrimSpace(data), "0x"), "0X")
	if hex == "" {
		return nil, fmt.Errorf("log data is empty; expected a uint256")
	}
	if len(hex) > 64 {
		return nil, fmt.Errorf("log data is %d hex digits, more than one uint256", len(hex))
	}

	value, ok := new(big.Int).SetString(hex, 16)
	if !ok {
		return nil, fmt.Errorf("log data %q is not hex", data)
	}
	return value, nil
}

// addressFromTopic pulls an indexed address parameter out of a topic.
//
// An indexed address occupies a full 32-byte word, zero-extended on the left, so
// the address is the last 20 bytes. Returned lowercase, matching how nodes report
// addresses and how asset_seeder stores them.
func addressFromTopic(topics []string, index int) (string, error) {
	if index >= len(topics) {
		return "", fmt.Errorf("log has %d topics, wanted index %d", len(topics), index)
	}

	hex := strings.TrimPrefix(strings.TrimPrefix(topics[index], "0x"), "0X")
	if len(hex) != 64 {
		return "", fmt.Errorf("topic %d is %d hex digits, want 64", index, len(hex))
	}

	return "0x" + strings.ToLower(hex[24:]), nil
}
