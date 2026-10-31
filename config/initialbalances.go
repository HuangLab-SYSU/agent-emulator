package config

import (
	"fmt"
	"math/big"

	"github.com/HuangLab-SYSU/block-emulator-x/pkg/core/account"
	"github.com/HuangLab-SYSU/block-emulator-x/pkg/utils"
)

// ParseInitialBalance accepts an explicit zero and full-width on-chain balances.
func ParseInitialBalance(value string) (*big.Int, error) {
	if value == "" {
		return nil, fmt.Errorf("initial balance must be a non-negative decimal integer")
	}

	for _, digit := range value {
		if digit < '0' || digit > '9' {
			return nil, fmt.Errorf("initial balance must be a non-negative decimal integer")
		}
	}

	balance, ok := new(big.Int).SetString(value, 10)
	if !ok || balance.BitLen() > 256 {
		return nil, fmt.Errorf("initial balance exceeds uint256")
	}

	return balance, nil
}

func ParseInitialBalances(values map[string]string) (map[account.Address]*big.Int, error) {
	result := make(map[account.Address]*big.Int, len(values))
	for encoded, value := range values {
		raw, err := utils.Hex2Bytes(encoded)
		if err != nil || len(raw) != 20 {
			return nil, fmt.Errorf("initial balance address must be 20-byte hex")
		}

		var address account.Address
		copy(address[:], raw)

		if _, exists := result[address]; exists {
			return nil, fmt.Errorf("duplicate initial balance address %s", encoded)
		}

		balance, err := ParseInitialBalance(value)
		if err != nil {
			return nil, fmt.Errorf("initial balance for %s: %w", encoded, err)
		}

		result[address] = balance
	}

	return result, nil
}
