package agentsupervisor

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestHybridRejectsTraceAfterAPILeaveAndContinues(t *testing.T) {
	h := testHybrid(t)
	now := time.Now()
	h.started = now
	require.NoError(t, h.injectTrace(context.Background(), now))
	_, err := h.applyDecision(context.Background(), Decision{Action: "leave"}, Observation{}, now)
	require.NoError(t, err)
	beforeTxs, beforeLinks, beforeMetrics, beforeSeq := len(h.compiler.txs), len(h.compiler.links), len(h.compiler.metrics), h.seq
	bob, err := h.compiler.registry.Active("bob")
	require.NoError(t, err)
	bobAddress, err := didAddress(bob.DID)
	require.NoError(t, err)
	beforeNonce := h.compiler.nonces[bobAddress]
	h.records = append(h.records, Record{Seq: 3, TS: 1000, AgentID: "bob", Target: "alice", Action: ActionPay, Amount: 3, RequestID: "invalid-after-api-leave"})
	require.NoError(t, h.injectTrace(context.Background(), now.Add(time.Second)))
	require.Equal(t, 3, h.cursor)
	require.Equal(t, 1, h.traceFailed)
	require.Equal(t, beforeTxs, len(h.compiler.txs))
	require.Equal(t, beforeLinks, len(h.compiler.links))
	require.Equal(t, beforeMetrics, len(h.compiler.metrics))
	require.Equal(t, beforeSeq, h.seq)
	require.Equal(t, beforeNonce, h.compiler.nonces[bobAddress])
	require.False(t, h.compiler.registry.agents["alice"].Active)
	dir := filepath.Join(h.cfg.Base.ResultDir, roundDirName(1))
	raw, err := os.ReadFile(filepath.Join(dir, TraceFailuresFile))
	require.NoError(t, err)
	var failure TraceFailure
	require.NoError(t, json.Unmarshal(raw, &failure))
	require.Equal(t, 3, failure.TraceLine)
	require.EqualValues(t, 1000, failure.Record.TS)
	require.Equal(t, "failed", failure.Status)
	require.Equal(t, "invalid-after-api-leave", failure.Record.RequestID)
	require.Contains(t, failure.Reason, `agent "alice" is not active`)
	input, err := os.ReadFile(filepath.Join(dir, "input_actions.jsonl"))
	require.NoError(t, err)
	require.NotContains(t, string(input), "invalid-after-api-leave")
	h.records = append(h.records,
		Record{Seq: 4, TS: 2000, AgentID: "carol", Action: ActionJoin},
		Record{Seq: 5, TS: 2000, AgentID: "bob", Target: "carol", Action: ActionPay, Amount: 4, RequestID: "valid-after-rejection"})
	require.NoError(t, h.injectTrace(context.Background(), now.Add(2*time.Second)))
	require.Equal(t, 5, h.cursor)
	require.Equal(t, beforeTxs+2, len(h.compiler.txs))
	require.Equal(t, beforeNonce, h.compiler.txs[len(h.compiler.txs)-1].Nonce, "failed trace did not consume sender nonce")
	require.NoError(t, h.persist())
	summary, err := os.ReadFile(filepath.Join(h.cfg.Base.ResultDir, HybridSummaryFile))
	require.NoError(t, err)
	var counts struct {
		Failed    int `json:"trace_actions_failed"`
		Remaining int `json:"trace_actions_remaining"`
	}
	require.NoError(t, json.Unmarshal(summary, &counts))
	require.Equal(t, 1, counts.Failed)
	require.Zero(t, counts.Remaining)
}

func TestHybridRejectsIllegalLifecycleActions(t *testing.T) {
	cases := []struct {
		name   string
		record Record
	}{
		{"inactive payer", Record{AgentID: "unknown", Target: "bob", Action: ActionPay, Amount: 1}},
		{"inactive payee", Record{AgentID: "bob", Target: "unknown", Action: ActionPay, Amount: 1}},
		{"inactive leave", Record{AgentID: "unknown", Action: ActionLeave}},
		{"zero payment", Record{AgentID: "alice", Target: "bob", Action: ActionPay}},
		{"unsupported", Record{AgentID: "bob", Action: "unknown"}},
	}
	for _, item := range cases {
		t.Run(item.name, func(t *testing.T) {
			h := testHybrid(t)
			now := time.Now()
			h.started = now
			require.NoError(t, h.injectTrace(context.Background(), now))
			record := item.record
			record.Seq = 3
			record.TS = 1000
			h.records = append(h.records, record, Record{Seq: 4, TS: 1000, AgentID: "carol", Action: ActionJoin})
			require.NoError(t, h.injectTrace(context.Background(), now.Add(time.Second)))
			require.Equal(t, 1, h.traceFailed)
			require.Equal(t, 4, h.cursor)
			require.Len(t, h.compiler.txs, 3)
			_, err := h.compiler.registry.Active("carol")
			require.NoError(t, err)
		})
	}
}

func TestHybridTraceFailureLogErrorsRemainFatal(t *testing.T) {
	h := testHybrid(t)
	now := time.Now()
	h.started = now
	require.NoError(t, h.injectTrace(context.Background(), now))
	writable := h.traceFailures
	// A readable but non-writable descriptor makes the append fail. Preserve both
	// descriptors for clean test cleanup without changing production permissions.
	readOnly, err := os.Open(filepath.Join(h.cfg.Base.ResultDir, roundDirName(1), TraceFailuresFile))
	require.NoError(t, err)
	h.traceFailures = readOnly
	t.Cleanup(func() { require.NoError(t, writable.Close()) })
	h.records = append(h.records, Record{Seq: 3, TS: 1000, AgentID: "bob", Target: "unknown", Action: ActionPay, Amount: 1})
	err = h.injectTrace(context.Background(), now.Add(time.Second))
	require.ErrorContains(t, err, "write trace failure")
	require.Equal(t, 2, h.cursor)
	require.Zero(t, h.traceFailed)
}

func TestHybridCompilerErrorsRemainFatal(t *testing.T) {
	h := testHybrid(t)
	h.compiler.cfg.Protocols.Identity.ContractAddress = "not-an-address"
	h.started = time.Now()
	err := h.injectTrace(context.Background(), h.started)
	require.ErrorContains(t, err, "contract address")
	require.Zero(t, h.cursor)
	require.Zero(t, h.traceFailed)
}
