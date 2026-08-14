package evm

import (
	"fmt"
	"strings"
)

// TopicTransfer is keccak256("Transfer(address indexed from, address indexed to,
// uint256 value)") — the ERC20 and TRC20 transfer event.
//
// Computed rather than copied, and it matches the published value.
const TopicTransfer = "0xddf252ad1be2c89b69c2b068fc378daa952ba7f163c4a11628f55a4df523b3ef"

// PadTopic left-pads a 20-byte address into the 32-byte form a topic filter
// needs.
//
// An indexed address parameter occupies a full word in the log's topic array,
// zero-extended on the left. Filtering by the unpadded address matches nothing —
// silently, since a filter that matches nothing is not an error.
//
// The result is lowercase: that is how nodes report topics, and a checksummed
// address would compare unequal.
func PadTopic(address string) (string, error) {
	hex := strings.TrimPrefix(strings.TrimPrefix(strings.TrimSpace(address), "0x"), "0X")
	if len(hex) != 40 {
		return "", fmt.Errorf("evm: %q is not a 20-byte address (got %d hex digits, want 40)",
			address, len(hex))
	}
	for _, r := range hex {
		if !isHexDigit(r) {
			return "", fmt.Errorf("evm: %q contains a non-hex character %q", address, r)
		}
	}

	return "0x" + strings.Repeat("0", 24) + strings.ToLower(hex), nil
}

func isHexDigit(r rune) bool {
	switch {
	case r >= '0' && r <= '9', r >= 'a' && r <= 'f', r >= 'A' && r <= 'F':
		return true
	default:
		return false
	}
}
