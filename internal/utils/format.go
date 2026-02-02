package utils

import (
	"math/big"
	"strings"
)

// FormatStaking formats a staking amount in wei to a human-readable string
// Converts wei to ETH and formats with thousand separators (no decimals)
// Examples:
//   - 1000000000000000000 -> "1"
//   - 1500000000000000000 -> "1"
//   - 1000000000000000000000 -> "1,000"
//   - 100000000000000000000000 -> "100,000"

func FormatStaking(wei *big.Int) string {
	if wei == nil || wei.Sign() == 0 {
		return "0"
	}

	// 1 ether = 10^18 wei
	// Convert to ETH (big.Int division to get integer part only)
	etherDivisor := new(big.Int).Exp(big.NewInt(10), big.NewInt(18), nil)
	ether := new(big.Int).Div(wei, etherDivisor)

	// Convert to string and add thousand separators
	etherStr := ether.String()
	
	// Add commas for thousands (from right to left)
	var formatted strings.Builder
	length := len(etherStr)
	for i, r := range etherStr {
		if i > 0 && (length-i)%3 == 0 {
			formatted.WriteString(",")
		}
		formatted.WriteRune(r)
	}
	
	return formatted.String()
}
