package agentsupervisor

import (
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"time"
)

// Chain-side measurement files, written by the BlockEmulator-X supervisor
// into the round's chain results directory.
const (
	relayDetailFileName = "relay_stats_detail_tx_info.csv"
	relayBriefFileName  = "relay_stats_brief_info.csv"
)

// LatencyStats summarizes a set of per-transaction latencies in milliseconds.
// The detail CSV quantizes timestamps to whole seconds, so single-tx values
// are coarse; the chain's own nanosecond TCL averages travel in ChainEpoch.
type LatencyStats struct {
	Count int     `json:"count"`
	AvgMs float64 `json:"avg_ms"`
	P50Ms float64 `json:"p50_ms"`
	P95Ms float64 `json:"p95_ms"`
	MaxMs float64 `json:"max_ms"`
}

// ChainEpoch passes one epoch row of relay_stats_brief_info.csv through,
// keeping the chain's nanosecond-precision TCL aggregates.
type ChainEpoch struct {
	EpochID        int     `json:"epoch_id"`
	TotalTx        float64 `json:"total_tx"`
	InnerShardTx   int     `json:"inner_shard_tx"`
	Relay1Tx       int     `json:"relay1_tx"`
	Relay2Tx       int     `json:"relay2_tx"`
	AvgTps         float64 `json:"avg_tps"`
	CtxRatio       float64 `json:"ctx_ratio"`
	AvgTclNs       float64 `json:"avg_tcl_ns"`
	AvgInnerTclNs  float64 `json:"avg_inner_tcl_ns"`
	AvgRelay1TclNs float64 `json:"avg_relay1_tcl_ns"`
	AvgRelay2TclNs float64 `json:"avg_relay2_tcl_ns"`
}

// RoundMetrics joins the planned actions with the chain's committed
// transactions and summarizes one executed round. It lands in
// rounds_summary.json via RoundResult.Metrics.
type RoundMetrics struct {
	// RoundWallSeconds covers the whole round (plan, chain run, CSV reads);
	// ChainWallSeconds covers the chain run alone. Both are set by the loop.
	RoundWallSeconds float64 `json:"round_wall_seconds"`
	ChainWallSeconds float64 `json:"chain_wall_seconds"`

	TxPlanned    int `json:"tx_planned"`
	TxCommitted  int `json:"tx_committed"`
	TxMissing    int `json:"tx_missing"`
	InnerShardTx int `json:"inner_shard_tx"`
	CrossShardTx int `json:"cross_shard_tx"`

	// ThroughputTps = TxCommitted / span(min create, max commit).
	ThroughputTps float64 `json:"throughput_tps"`

	LatencyMs       *LatencyStats            `json:"latency_ms,omitempty"`
	LatencyByAction map[string]*LatencyStats `json:"latency_ms_by_action,omitempty"`
	ChainEpochs     []ChainEpoch             `json:"chain_epochs,omitempty"`
}

// AggregateRoundMetrics reads the chain measurement CSVs from chainResultDir
// and joins them with the planned action links by transaction hash: every
// committed transaction is attributed back to the action that compiled it,
// and planned hashes absent from the chain are counted as missing.
func AggregateRoundMetrics(links []ActionTxLink, chainResultDir string) (*RoundMetrics, error) {
	rows, err := readRelayDetail(filepath.Join(chainResultDir, relayDetailFileName))
	if err != nil {
		return nil, err
	}

	epochs, err := readRelayBrief(filepath.Join(chainResultDir, relayBriefFileName))
	if err != nil {
		return nil, err
	}

	actionOf := make(map[string]string)
	planned := make(map[string]struct{})
	committed := make(map[string]struct{}, len(rows))

	for _, link := range links {
		for _, hash := range link.TxHashes {
			actionOf[hash] = string(link.Action)
			planned[hash] = struct{}{}
		}
	}

	for _, row := range rows {
		committed[row.hash] = struct{}{}
	}

	m := &RoundMetrics{
		TxPlanned:   len(planned),
		TxCommitted: len(rows),
		ChainEpochs: epochs,
	}

	latencies := make([]float64, 0, len(rows))
	byAction := make(map[string][]float64)
	minCreate, maxCommit := time.Time{}, time.Time{}

	for _, row := range rows {
		if _, ok := planned[row.hash]; !ok {
			return nil, fmt.Errorf("chain reported unknown transaction %s", row.hash)
		}

		if row.crossShard {
			m.CrossShardTx++
		} else {
			m.InnerShardTx++
		}

		if minCreate.IsZero() || row.created.Before(minCreate) {
			minCreate = row.created
		}

		if row.committed.After(maxCommit) {
			maxCommit = row.committed
		}

		ms := row.committed.Sub(row.created).Seconds() * 1000
		latencies = append(latencies, ms)
		byAction[actionOf[row.hash]] = append(byAction[actionOf[row.hash]], ms)
	}

	for hash := range planned {
		if _, ok := committed[hash]; !ok {
			m.TxMissing++
		}
	}

	if len(latencies) > 0 {
		m.LatencyMs = summarize(latencies)
		m.LatencyByAction = make(map[string]*LatencyStats, len(byAction))

		for action, samples := range byAction {
			m.LatencyByAction[action] = summarize(samples)
		}

		span := maxCommit.Sub(minCreate).Seconds()
		if span > 0 {
			m.ThroughputTps = float64(len(rows)) / span
		}
	}

	return m, nil
}

type relayDetailRow struct {
	hash       string
	created    time.Time
	committed  time.Time
	crossShard bool
}

func readRelayDetail(path string) ([]relayDetailRow, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open relay detail csv: %w", err)
	}

	defer func() { _ = f.Close() }()

	r := csv.NewReader(f)

	header, err := r.Read()
	if err != nil {
		return nil, fmt.Errorf("read relay detail header: %w", err)
	}

	if len(header) < 4 {
		return nil, fmt.Errorf("relay detail csv has %d columns, want at least 4", len(header))
	}

	var rows []relayDetailRow

	for line := 2; ; line++ {
		rec, err := r.Read()
		if errors.Is(err, io.EOF) {
			break
		}

		if err != nil {
			return nil, fmt.Errorf("read relay detail line %d: %w", line, err)
		}

		if len(rec) < 4 {
			return nil, fmt.Errorf("relay detail line %d has %d columns, want at least 4", line, len(rec))
		}

		created, err := time.Parse(time.RFC3339, rec[1])
		if err != nil {
			return nil, fmt.Errorf("parse relay detail line %d create time: %w", line, err)
		}

		committed, err := time.Parse(time.RFC3339, rec[2])
		if err != nil {
			return nil, fmt.Errorf("parse relay detail line %d commit time: %w", line, err)
		}

		rows = append(rows, relayDetailRow{
			hash:       rec[0],
			created:    created,
			committed:  committed,
			crossShard: rec[3] == "true",
		})
	}

	return rows, nil
}

func readRelayBrief(path string) ([]ChainEpoch, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open relay brief csv: %w", err)
	}

	defer func() { _ = f.Close() }()

	r := csv.NewReader(f)

	header, err := r.Read()
	if err != nil {
		return nil, fmt.Errorf("read relay brief header: %w", err)
	}

	if len(header) < 13 {
		return nil, fmt.Errorf("relay brief csv has %d columns, want at least 13", len(header))
	}

	var epochs []ChainEpoch

	for line := 2; ; line++ {
		rec, err := r.Read()
		if errors.Is(err, io.EOF) {
			break
		}

		if err != nil {
			return nil, fmt.Errorf("read relay brief line %d: %w", line, err)
		}

		if len(rec) < 13 {
			return nil, fmt.Errorf("relay brief line %d has %d columns, want at least 13", line, len(rec))
		}

		epochID, err := strconv.Atoi(rec[0])
		if err != nil {
			return nil, fmt.Errorf("parse relay brief line %d epoch id: %w", line, err)
		}

		total, err := strconv.ParseFloat(rec[1], 64)
		if err != nil {
			return nil, fmt.Errorf("parse relay brief line %d total tx: %w", line, err)
		}

		inner, relay1, relay2, err := parseThreeInts(rec[2], rec[3], rec[4])
		if err != nil {
			return nil, fmt.Errorf("parse relay brief line %d tx counts: %w", line, err)
		}

		tps, ctxRatio, tcl, innerTcl, relay1Tcl, relay2Tcl, err := parseSixFloats(
			rec[7], rec[8], rec[9], rec[10], rec[11], rec[12],
		)
		if err != nil {
			return nil, fmt.Errorf("parse relay brief line %d aggregates: %w", line, err)
		}

		epochs = append(epochs, ChainEpoch{
			EpochID:        epochID,
			TotalTx:        total,
			InnerShardTx:   inner,
			Relay1Tx:       relay1,
			Relay2Tx:       relay2,
			AvgTps:         tps,
			CtxRatio:       ctxRatio,
			AvgTclNs:       tcl,
			AvgInnerTclNs:  innerTcl,
			AvgRelay1TclNs: relay1Tcl,
			AvgRelay2TclNs: relay2Tcl,
		})
	}

	return epochs, nil
}

// summarize sorts samples in place and returns count, mean and nearest-rank
// percentiles.
func summarize(samples []float64) *LatencyStats {
	sort.Float64s(samples)

	stats := &LatencyStats{Count: len(samples), MaxMs: samples[len(samples)-1]}

	sum := 0.0

	for _, s := range samples {
		sum += s
	}

	stats.AvgMs = sum / float64(len(samples))
	stats.P50Ms = nearestRank(samples, 0.50)
	stats.P95Ms = nearestRank(samples, 0.95)

	return stats
}

func nearestRank(sorted []float64, p float64) float64 {
	idx := int(p*float64(len(sorted)-1) + 0.5)
	if idx > len(sorted)-1 {
		idx = len(sorted) - 1
	}

	return sorted[idx]
}

func parseThreeInts(a, b, c string) (int, int, int, error) {
	x, err := strconv.Atoi(a)
	if err != nil {
		return 0, 0, 0, err
	}

	y, err := strconv.Atoi(b)
	if err != nil {
		return 0, 0, 0, err
	}

	z, err := strconv.Atoi(c)
	if err != nil {
		return 0, 0, 0, err
	}

	return x, y, z, nil
}

func parseSixFloats(vals ...string) (float64, float64, float64, float64, float64, float64, error) {
	out := make([]float64, len(vals))

	for i, v := range vals {
		f, err := strconv.ParseFloat(v, 64)
		if err != nil {
			return 0, 0, 0, 0, 0, 0, err
		}

		out[i] = f
	}

	return out[0], out[1], out[2], out[3], out[4], out[5], nil
}
