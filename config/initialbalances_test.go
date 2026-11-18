package config

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestParseInitialBalance(t *testing.T) {
	for _, value := range []string{"0", "2", "1000000000000000000000000000000000000"} {
		amount, err := ParseInitialBalance(value)
		require.NoError(t, err)
		require.Equal(t, value, amount.String())
	}
	for _, value := range []string{"", "-1", "+2", "1.5", " 2", "1e3", strings.Repeat("9", 79)} {
		_, err := ParseInitialBalance(value)
		require.Error(t, err)
	}
	_, err := ParseInitialBalances(map[string]string{"0x01": "2"})
	require.Error(t, err)
	_, err = ParseInitialBalances(map[string]string{"0000000000000000000000000000000000000001": "2", "0x0000000000000000000000000000000000000001": "3"})
	require.ErrorContains(t, err, "duplicate")
}
