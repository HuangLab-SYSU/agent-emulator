package agentsupervisor

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

const detailFixture = `OriginalHash,Tx create time,Tx finally commit time,Is cross-shard tx or not,Inner shard tx block propose time,Relay1 block propose time,Relay1 tx commit time,Relay2 block propose time,Relay2 tx commit time
aa,2026-09-18T10:00:00+08:00,2026-09-18T10:00:03+08:00,true,,2026-09-18T10:00:01+08:00,2026-09-18T10:00:02+08:00,2026-09-18T10:00:03+08:00,2026-09-18T10:00:03+08:00
bb,2026-09-18T10:00:01+08:00,2026-09-18T10:00:02+08:00,false,2026-09-18T10:00:01+08:00,,,,
cc,2026-09-18T10:00:01+08:00,2026-09-18T10:00:05+08:00,false,2026-09-18T10:00:02+08:00,,,,
`

const briefFixture = `EpochID,Total tx # in this epoch,Inner-shard tx # in this epoch,Relay1 tx # in this epoch,Relay2 tx # in this epoch,Epoch start time,Epoch end time,Avg. TPS of this epoch (txs per second),CTX ratio of this epoch,Avg. TCL of this epoch (nanosecond),Avg. inner-shard TCL of this epoch (nanosecond),Avg. relay1 TCL of this epoch (nanosecond),Avg. relay2 TCL of this epoch (nanosecond)
0,3.00,2,1,1,2026-09-18T10:00:00+08:00,2026-09-18T10:00:05+08:00,0.60,0.33,2000000000.00,1500000000.00,1000000000.00,500000000.00
`

func fixtureLinks() []ActionTxLink {
	return []ActionTxLink{
		{Seq: 1, Action: ActionJoin, AgentID: "alice", TxHashes: []string{"aa"}},
		{Seq: 2, Action: ActionPay, AgentID: "alice", Target: "bob", RequestID: "p1", TxHashes: []string{"bb"}},
		{Seq: 3, Action: ActionRawTx, RequestID: "raw-1", TxHashes: []string{"cc"}},
		{Seq: 4, Action: ActionPay, AgentID: "bob", Target: "alice", RequestID: "p2", TxHashes: []string{"dd"}},
	}
}

func writeChainFixtures(t *testing.T, detail, brief string) string {
	t.Helper()

	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, relayDetailFileName), []byte(detail), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(dir, relayBriefFileName), []byte(brief), 0o600))

	return dir
}

func TestAggregateRoundMetricsReconcilesAndSummarizes(t *testing.T) {
	dir := writeChainFixtures(t, detailFixture, briefFixture)

	m, err := AggregateRoundMetrics(fixtureLinks(), dir)
	require.NoError(t, err)

	// Reconciliation: dd is planned but never committed.
	require.Equal(t, 4, m.TxPlanned)
	require.Equal(t, 3, m.TxCommitted)
	require.Equal(t, 1, m.TxMissing)
	require.Equal(t, 2, m.InnerShardTx)
	require.Equal(t, 1, m.CrossShardTx)

	// Latencies: aa=3s, bb=1s, cc=4s.
	require.NotNil(t, m.LatencyMs)
	require.Equal(t, 3, m.LatencyMs.Count)
	require.InDelta(t, 2666.67, m.LatencyMs.AvgMs, 0.01)
	require.EqualValues(t, 3000, m.LatencyMs.P50Ms)
	require.EqualValues(t, 4000, m.LatencyMs.P95Ms)
	require.EqualValues(t, 4000, m.LatencyMs.MaxMs)

	require.Len(t, m.LatencyByAction, 3)
	require.EqualValues(t, 3000, m.LatencyByAction["join"].MaxMs)
	require.EqualValues(t, 1000, m.LatencyByAction["pay"].MaxMs)
	require.EqualValues(t, 4000, m.LatencyByAction["raw_tx"].MaxMs)

	// Span: min create 10:00:00 -> max commit 10:00:05 = 5s for 3 txs.
	require.InDelta(t, 0.6, m.ThroughputTps, 1e-9)

	// The brief epoch passes through with its nanosecond TCLs.
	require.Len(t, m.ChainEpochs, 1)
	epoch := m.ChainEpochs[0]
	require.Equal(t, 0, epoch.EpochID)
	require.InDelta(t, 3.0, epoch.TotalTx, 1e-9)
	require.Equal(t, 2, epoch.InnerShardTx)
	require.InDelta(t, 0.60, epoch.AvgTps, 1e-9)
	require.InDelta(t, 0.33, epoch.CtxRatio, 1e-9)
	require.InDelta(t, 2e9, epoch.AvgTclNs, 1e-3)
	require.InDelta(t, 1.5e9, epoch.AvgInnerTclNs, 1e-3)

	// The loop sets the wall clocks; aggregation leaves them zero here.
	require.Zero(t, m.RoundWallSeconds)
	require.Zero(t, m.ChainWallSeconds)
}

func TestAggregateRoundMetricsRejectsBadInput(t *testing.T) {
	links := fixtureLinks()

	t.Run("missing results dir", func(t *testing.T) {
		_, err := AggregateRoundMetrics(links, filepath.Join(t.TempDir(), "nowhere"))
		require.ErrorContains(t, err, "open relay detail csv")
	})

	t.Run("unparseable timestamp", func(t *testing.T) {
		bad := "OriginalHash,Tx create time,Tx finally commit time,Is cross-shard tx or not\naa,yesterday,sometime,false\n"
		dir := writeChainFixtures(t, bad, briefFixture)
		_, err := AggregateRoundMetrics(links, dir)
		require.ErrorContains(t, err, "create time")
	})

	t.Run("unknown committed hash", func(t *testing.T) {
		unknown := "OriginalHash,Tx create time,Tx finally commit time,Is cross-shard tx or not\nzz,2026-09-18T10:00:00+08:00,2026-09-18T10:00:01+08:00,false\n"
		dir := writeChainFixtures(t, unknown, briefFixture)
		_, err := AggregateRoundMetrics(links, dir)
		require.ErrorContains(t, err, "unknown transaction zz")
	})

	t.Run("missing brief file", func(t *testing.T) {
		dir := t.TempDir()
		require.NoError(t, os.WriteFile(filepath.Join(dir, relayDetailFileName), []byte(detailFixture), 0o600))
		_, err := AggregateRoundMetrics(links, dir)
		require.ErrorContains(t, err, "open relay brief csv")
	})
}
