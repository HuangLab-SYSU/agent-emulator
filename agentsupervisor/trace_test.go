package agentsupervisor

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func writeTrace(t *testing.T, lines ...string) string {
	t.Helper()

	path := filepath.Join(t.TempDir(), "trace.jsonl")
	require.NoError(t, os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o600))

	return path
}

func TestLoadTraceKeepsPlainTransfersAtTheirFilePosition(t *testing.T) {
	path := writeTrace(t,
		`{"agent_id":"alice","action":"join","params_hash":"doc","ts":1}`,
		`{"agent_id":"alice","target":"bob","action":"pay","amount":5,"ts":2}`,
		`{"sender":"0x`+strings.Repeat("11", 20)+`","recipient":"0x`+strings.Repeat("22", 20)+`","value":"680"}`,
		`{"agent_id":"bob","action":"leave","params_hash":"exit","ts":3}`,
	)

	records, err := LoadTrace(path)
	require.NoError(t, err)
	require.Len(t, records, 4)

	// The plain transfer inherits the previous line's ts, so the stable
	// (TS, Seq) sort keeps it between the pay and the leave.
	require.Equal(t, ActionPay, records[1].Action)
	require.Equal(t, ActionRawTx, records[2].Action)
	require.Equal(t, ActionLeave, records[3].Action)
	require.EqualValues(t, 2, records[2].TS)
	require.Equal(t, 3, records[2].Seq)

	spec := records[2].RawTx
	require.NotNil(t, spec)
	require.Equal(t, "0x"+strings.Repeat("11", 20), spec.Sender)
	require.Equal(t, "0x"+strings.Repeat("22", 20), spec.Recipient)
	require.Equal(t, "680", spec.Value)
}

func TestLoadTraceIgnoresExtraFieldsOnPlainTransfers(t *testing.T) {
	path := writeTrace(t,
		`{"agent_id":"alice","action":"join","params_hash":"doc","ts":1}`,
		`{"hash":"ab","sender":"0x`+strings.Repeat("11", 20)+`","recipient":"0x`+strings.Repeat("22", 20)+`","value":"7","nonce":9,"data":"0xdeadbeef"}`,
	)

	records, err := LoadTrace(path)
	require.NoError(t, err)
	require.Len(t, records, 2)
	require.Equal(t, ActionRawTx, records[1].Action)
	require.Equal(t, "7", records[1].RawTx.Value)
}

func TestLoadTraceRejectsBadPlainTransfers(t *testing.T) {
	t.Run("ambiguous line", func(t *testing.T) {
		path := writeTrace(t,
			`{"agent_id":"alice","action":"pay","sender":"0x`+strings.Repeat("11", 20)+`","recipient":"0x`+strings.Repeat("22", 20)+`","value":"1"}`,
		)
		_, err := LoadTrace(path)
		require.ErrorContains(t, err, "ambiguous")
	})

	t.Run("missing fields", func(t *testing.T) {
		path := writeTrace(t, `{"sender":"0x`+strings.Repeat("11", 20)+`"}`)
		_, err := LoadTrace(path)
		require.ErrorContains(t, err, "sender, recipient and value are required")
	})
}
