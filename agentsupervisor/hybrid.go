package agentsupervisor

import (
	"bytes"
	"context"
	"encoding/gob"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math/big"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"time"

	"github.com/HuangLab-SYSU/block-emulator-x/config"
	"github.com/HuangLab-SYSU/block-emulator-x/pkg/core/account"
	"github.com/HuangLab-SYSU/block-emulator-x/pkg/core/transaction"
	"github.com/HuangLab-SYSU/block-emulator-x/pkg/message"
	"github.com/HuangLab-SYSU/block-emulator-x/pkg/network"
	"github.com/HuangLab-SYSU/block-emulator-x/pkg/network/rpcserver"
	"github.com/HuangLab-SYSU/block-emulator-x/pkg/nodetopo"
	"github.com/HuangLab-SYSU/block-emulator-x/pkg/partition"
)

const HybridSummaryFile = "hybrid_summary.json"

var errInvalidDecision = errors.New("invalid agent decision")

const (
	phaseIdle               = "idle"
	phaseThinking           = "thinking"
	phaseQuiesce            = "quiesce"
	phaseSnapshot           = "snapshot"
	phaseValidationQuiesce  = "validation_quiesce"
	phaseValidationSnapshot = "validation_snapshot"
	phaseFinalSnapshot      = "final_snapshot"
	phaseFinished           = "finished"
)

type pendingPayment struct {
	Tx     transaction.Transaction
	Debit  bool
	Credit bool
}
type modelOutcome struct {
	Reply ModelReply
	Err   error
}

type DecisionAudit struct {
	Observation     Observation `json:"observation"`
	Reply           ModelReply  `json:"reply"`
	Started         time.Time   `json:"started"`
	Finished        time.Time   `json:"finished"`
	DurationMS      int64       `json:"duration_ms"`
	BlocksGranted   uint64      `json:"blocks_granted"`
	BlocksCommitted uint64      `json:"blocks_committed"`
	Result          string      `json:"result"`
	TransactionHash string      `json:"transaction_hash,omitempty"`
}

// HybridRuntime implements the supervisor committee contract. All mutable chain,
// trace and budget state is confined to its serial supervisor loop. Only the
// blocking model request runs in a separate goroutine, returning via a channel.
type HybridRuntime struct {
	cfg            Config
	chainCfg       config.SupervisorCfg
	compiler       *AgentSupervisor
	conn           *network.ConnHandler
	resolver       nodetopo.NodeMapper
	model          DecisionModel
	budget         *BlockBudget
	records        []Record
	cursor         int
	skipped        int
	firstTS        int64
	seq            int
	started        time.Time
	nextDecision   time.Time
	closing        bool
	done           bool
	phase          string
	phaseStarted   time.Time
	snapshotID     uint64
	snapshots      map[int64]*message.AccountSnapshotResponse
	audits         []DecisionAudit
	current        DecisionAudit
	outcome        modelOutcome
	resultCh       chan modelOutcome
	cancel         context.CancelFunc
	decision       uint64
	grantBaseline  uint64
	commitBaseline uint64
	pending        map[string]*pendingPayment
	apiPending     string
	latest         Observation
	events         *os.File
	thinkingFile   *os.File
	thinkingLogger *slog.Logger
}

func NewHybridRuntime(
	conn *network.ConnHandler,
	resolver nodetopo.NodeMapper,
	chainCfg config.SupervisorCfg,
) (*HybridRuntime, error) {
	if chainCfg.ConsensusType != config.StaticRelayConsensus {
		return nil, fmt.Errorf("hybrid runtime requires static_relay")
	}

	cfg, err := LoadConfig(chainCfg.AgentRuntimeConfig)
	if err != nil {
		return nil, err
	}

	if !cfg.AgentAPI.Enabled {
		return nil, fmt.Errorf("agent runtime config must enable agent_api")
	}

	if chainCfg.TxInjectionSpeed <= 0 {
		return nil, fmt.Errorf("hybrid runtime requires positive tx_injection_speed")
	}

	records, err := LoadTrace(cfg.Experiment.Trace)
	if err != nil {
		return nil, err
	}

	records, skipped, err := FilterHybridTrace(records, cfg.AgentAPI.AgentID, cfg.Experiment.Seed)
	if err != nil {
		return nil, err
	}
	// Never silently continue with a registry from a previous independent run.
	registryPath := filepath.Join(cfg.Base.ResultDir, RegistryFileName)
	if _, err := os.Stat(registryPath); err == nil {
		return nil, fmt.Errorf("hybrid runtime requires a fresh result_dir: registry already exists")
	} else if !os.IsNotExist(err) {
		return nil, fmt.Errorf("check registry: %w", err)
	}

	compiler, err := NewAgentSupervisor(cfg)
	if err != nil {
		return nil, err
	}

	dir := filepath.Join(cfg.Base.ResultDir, roundDirName(1))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("create hybrid output: %w", err)
	}

	events, err := os.OpenFile(filepath.Join(dir, "input_actions.jsonl"), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, fmt.Errorf("create hybrid trace: %w", err)
	}

	thinkingLogger, thinkingFile, err := newThinkingLogger(cfg.Base.ResultDir, cfg.AgentAPI.AgentID, cfg.AgentAPI.Model)
	if err != nil {
		return nil, errors.Join(err, events.Close())
	}

	runtime := &HybridRuntime{
		cfg:            cfg,
		chainCfg:       chainCfg,
		compiler:       compiler,
		conn:           conn,
		resolver:       resolver,
		model:          &ChatModel{Config: cfg.AgentAPI},
		budget:         NewBlockBudget(chainCfg.ShardNum),
		records:        records,
		skipped:        skipped,
		phase:          phaseIdle,
		pending:        make(map[string]*pendingPayment),
		resultCh:       make(chan modelOutcome, 1),
		events:         events,
		thinkingFile:   thinkingFile,
		thinkingLogger: thinkingLogger,
	}
	if len(records) > 0 {
		runtime.firstTS = records[0].TS
	}

	slog.Info(
		"hybrid agent enabled",
		"agent_id",
		cfg.AgentAPI.AgentID,
		"model",
		cfg.AgentAPI.Model,
		"trace_actions_replaced",
		skipped,
		"max_blocks_during_decision",
		*cfg.AgentAPI.MaxBlocksDuringDecision,
	)

	return runtime, nil
}

// FilterHybridTrace keeps the API identity's first join but replaces its later
// actions. Plain transfers may not impersonate that API-controlled account.
func FilterHybridTrace(records []Record, agentID string, seed int64) ([]Record, int, error) {
	addr, err := didAddress(allocatedDID(seed, agentID))
	if err != nil {
		return nil, 0, err
	}

	var filtered []Record

	joined := false
	skipped := 0

	for _, record := range records {
		if record.RawTx != nil {
			sender, err := rawTxAddr(record.RawTx.Sender, "sender")
			if err != nil {
				return nil, 0, err
			}

			if sender == addr {
				return nil, 0, fmt.Errorf("trace line %d: raw transfer cannot spend API agent's account", record.Seq)
			}
		}

		if record.AgentID == agentID {
			if record.Action == ActionJoin && !joined {
				joined = true
			} else {
				skipped++
				continue
			}
		}

		filtered = append(filtered, record)
	}

	if !joined {
		return nil, 0, fmt.Errorf("API agent %q must have a join in trace", agentID)
	}

	return filtered, skipped, nil
}

func decodeRuntime(payload []byte, value any) error {
	if err := gob.NewDecoder(bytes.NewReader(payload)).Decode(value); err != nil {
		return fmt.Errorf("decode runtime message: %w", err)
	}

	return nil
}

func (h *HybridRuntime) HandleMsg(_ context.Context, msg *rpcserver.WrappedMsg) error {
	switch msg.GetMsgType() {
	case message.ProposalPermitRequestType:
		var req message.ProposalPermitRequest
		if err := decodeRuntime(msg.GetPayload(), &req); err != nil {
			return err
		}

		return h.budget.Request(req.ShardID, req.ID)
	case message.ProposalPermitDoneType:
		var done message.ProposalPermitDone
		if err := decodeRuntime(msg.GetPayload(), &done); err != nil {
			return err
		}

		return h.budget.Complete(done.ShardID, done.ID)
	case message.AccountSnapshotResponseType:
		var response message.AccountSnapshotResponse
		if err := decodeRuntime(msg.GetPayload(), &response); err != nil {
			return err
		}

		if response.ID != h.snapshotID ||
			(h.phase != phaseSnapshot && h.phase != phaseValidationSnapshot && h.phase != phaseFinalSnapshot) {
			return nil
		}

		if response.ShardID < 0 || response.ShardID >= h.chainCfg.ShardNum {
			return fmt.Errorf("invalid snapshot shard")
		}

		if response.Error != "" {
			return fmt.Errorf("snapshot shard %d: %s", response.ShardID, response.Error)
		}

		h.snapshots[response.ShardID] = &response
	case message.RelayBlockInfoMessageType:
		var info message.RelayBlockInfoMsg
		if err := decodeRuntime(msg.GetPayload(), &info); err != nil {
			return err
		}

		for _, tx := range info.InnerShardTxs {
			hash, err := tx.Hash()
			if err != nil {
				return fmt.Errorf("hash committed tx: %w", err)
			}

			delete(h.pending, hex.EncodeToString(hash))
		}

		for _, tx := range info.Relay1Txs {
			if p := h.pending[hex.EncodeToString(tx.ROriginalHash)]; p != nil {
				p.Debit = true
			}
		}

		for _, tx := range info.Relay2Txs {
			if p := h.pending[hex.EncodeToString(tx.ROriginalHash)]; p != nil {
				p.Credit = true
			}
		}

		for hash, p := range h.pending {
			if p.Debit && p.Credit {
				delete(h.pending, hash)
			}
		}
	}

	return nil
}

func (h *HybridRuntime) SendTxsAndConsensus(ctx context.Context) error {
	now := time.Now()
	if h.started.IsZero() {
		h.started = now
		h.nextDecision = now
	}

	if err := h.updateLifecycle(now); err != nil {
		return err
	}

	if h.apiPending != "" && h.pending[h.apiPending] == nil {
		h.apiPending = ""
	}

	if h.phase == phaseIdle || h.phase == phaseThinking {
		if !h.closing {
			if err := h.injectTrace(ctx, now); err != nil {
				return err
			}
		}
	}

	if err := h.pollModel(now); err != nil {
		return err
	}

	if !h.closing && h.phase == phaseIdle && !now.Before(h.nextDecision) && h.apiPending == "" && h.apiReady() {
		h.phase = phaseQuiesce
		h.phaseStarted = now
		h.budget.Hold()
	}

	if err := h.advanceSnapshots(ctx, now); err != nil {
		return err
	}

	if h.phase != phaseIdle && h.phase != phaseThinking && h.phase != phaseFinished &&
		now.Sub(h.phaseStarted) > time.Duration(h.cfg.AgentAPI.DrainTimeoutSeconds)*time.Second {
		return fmt.Errorf("runtime %s timeout", h.phase)
	}

	if h.closing && h.phase == phaseIdle && len(h.pending) == 0 {
		h.budget.Hold()

		if h.budget.Drained() {
			if err := h.requestSnapshot(ctx, phaseFinalSnapshot, now); err != nil {
				return err
			}
		}
	}

	for {
		shard, id, ok := h.budget.Grant()
		if !ok {
			break
		}

		if err := h.send(ctx, shard, &message.ProposalPermitGrant{ID: id}); err != nil {
			return err
		}
	}

	return nil
}

func (h *HybridRuntime) apiReady() bool {
	agent, err := h.compiler.registry.Active(h.cfg.AgentAPI.AgentID)
	if err != nil {
		return false
	}

	addr, err := didAddress(agent.DID)
	if err != nil {
		return false
	}

	for _, p := range h.pending {
		if p.Tx.Sender == addr {
			return false
		}
	}

	return true
}

func (h *HybridRuntime) injectTrace(ctx context.Context, now time.Time) error {
	elapsed := now.Sub(h.started).Seconds() * 1000
	for n := int64(0); h.cursor < len(h.records) && n < h.chainCfg.TxInjectionSpeed; n++ {
		record := h.records[h.cursor]

		due := float64(record.TS-h.firstTS) / h.cfg.AgentAPI.TraceTimeScale
		if due > elapsed {
			break
		}

		if _, err := h.submitRecord(ctx, record, now); err != nil {
			return fmt.Errorf("trace line %d: %w", record.Seq, err)
		}

		h.cursor++
	}

	return nil
}

func (h *HybridRuntime) submitRecord(ctx context.Context, record Record, now time.Time) (string, error) {
	h.seq++
	record.Seq = h.seq
	record.TS = now.UnixMilli()
	count := len(h.compiler.txs)

	result, err := h.compiler.Process([]Record{record})
	if err != nil {
		return "", err
	}
	// Keep the original trace wire representation for raw transfers.
	var event any = record
	if record.RawTx != nil {
		event = record.RawTx
	}

	if err := json.NewEncoder(h.events).Encode(event); err != nil {
		return "", fmt.Errorf("persist input action: %w", err)
	}

	txs := result.Transactions[count:]

	var lastHash string

	byShard := make([][]transaction.Transaction, h.chainCfg.ShardNum)

	for _, tx := range txs {
		hash, err := tx.Hash()
		if err != nil {
			return "", fmt.Errorf("hash submitted tx: %w", err)
		}

		lastHash = hex.EncodeToString(hash)
		h.pending[lastHash] = &pendingPayment{Tx: tx}
		shard := partition.DefaultAccountLoc(tx.Sender, h.chainCfg.ShardNum)
		byShard[shard] = append(byShard[shard], tx)
	}

	if err := message.SendWrappedTxs2Shards(ctx, byShard, h.conn, h.resolver); err != nil {
		return "", err
	}

	return lastHash, nil
}

func (h *HybridRuntime) send(ctx context.Context, shard int64, value any) error {
	leader, err := h.resolver.GetLeader(shard)
	if err != nil {
		return fmt.Errorf("resolve shard leader: %w", err)
	}

	msg, err := message.WrapMsg(value)
	if err != nil {
		return err
	}

	h.conn.SendMsg2Dest(ctx, leader, msg)

	return nil
}

func (h *HybridRuntime) requestSnapshot(ctx context.Context, phase string, now time.Time) error {
	h.snapshotID++
	h.snapshots = make(map[int64]*message.AccountSnapshotResponse)
	h.phase = phase
	h.phaseStarted = now

	addresses := make([][]account.Address, h.chainCfg.ShardNum)
	for _, agent := range h.compiler.registry.agents {
		addr, err := didAddress(agent.DID)
		if err != nil {
			return err
		}

		shard := partition.DefaultAccountLoc(addr, h.chainCfg.ShardNum)
		addresses[shard] = append(addresses[shard], addr)
	}

	for shard := int64(0); shard < h.chainCfg.ShardNum; shard++ {
		if err := h.send(
			ctx,
			shard,
			&message.AccountSnapshotRequest{ID: h.snapshotID, Addresses: addresses[shard]},
		); err != nil {
			return err
		}
	}

	return nil
}

func (h *HybridRuntime) buildObservation(now time.Time) (Observation, error) {
	obs := Observation{
		AgentID:             h.cfg.AgentAPI.AgentID,
		Decision:            h.decision,
		ObservedAt:          now,
		Heights:             make(map[int64]uint64),
		PendingTransactions: len(h.pending),
	}
	obs.LatestBlocks = make(map[int64]message.SnapshotBlock)
	balances := make(map[account.Address]*big.Int)

	for shard, snapshot := range h.snapshots {
		obs.Heights[shard] = snapshot.Height

		obs.LatestBlocks[shard] = snapshot.Block
		for _, state := range snapshot.States {
			if state == nil || state.Balance == nil {
				return obs, fmt.Errorf("snapshot contains missing state")
			}

			balances[state.Address] = new(big.Int).Set(state.Balance)
		}
	}

	ids := make([]string, 0, len(h.compiler.registry.agents))
	for id := range h.compiler.registry.agents {
		ids = append(ids, id)
	}

	sort.Strings(ids)

	for _, id := range ids {
		agent := h.compiler.registry.agents[id]

		addr, err := didAddress(agent.DID)
		if err != nil {
			return obs, err
		}

		balance := balances[addr]
		if balance == nil {
			return obs, fmt.Errorf("snapshot missing agent %s", id)
		}

		available := new(big.Int).Set(balance)

		for _, p := range h.pending {
			if p.Tx.Sender == addr && !p.Debit {
				available.Sub(available, p.Tx.Value)
			}
		}

		if available.Sign() < 0 {
			available.SetInt64(0)
		}

		obs.Agents = append(
			obs.Agents,
			ObservedAgent{
				AgentID:          id,
				Active:           agent.Active,
				Balance:          balance.String(),
				AvailableBalance: available.String(),
			},
		)
	}

	if len(h.audits) > 0 {
		obs.PreviousResult = h.audits[len(h.audits)-1].Result
	}

	return obs, nil
}

func (h *HybridRuntime) startDecision(obs Observation, now time.Time) {
	h.decision++
	obs.Decision = h.decision
	h.latest = obs
	h.current = DecisionAudit{Observation: obs, Started: now}
	h.grantBaseline = h.budget.Granted
	h.commitBaseline = h.budget.Committed
	h.budget.Limit(*h.cfg.AgentAPI.MaxBlocksDuringDecision)
	h.phase = phaseThinking
	ctx, cancel := context.WithTimeout(
		context.Background(),
		time.Duration(h.cfg.AgentAPI.DecisionTimeoutSeconds)*time.Second,
	)
	h.cancel = cancel
	h.logThinkingStarted(obs)

	go func() { reply, err := h.model.Decide(ctx, obs); h.resultCh <- modelOutcome{Reply: reply, Err: err} }()
}

func (h *HybridRuntime) applyDecision(
	ctx context.Context,
	decision Decision,
	obs Observation,
	now time.Time,
) (string, error) {
	if decision.Action == "wait" {
		if decision.Target != "" || decision.Amount != "" {
			return "", fmt.Errorf("%w: wait cannot include target or amount", errInvalidDecision)
		}

		return "", nil
	}

	if _, err := h.compiler.registry.Active(h.cfg.AgentAPI.AgentID); err != nil {
		return "", fmt.Errorf("%w: %w", errInvalidDecision, err)
	}

	record := Record{
		AgentID:   h.cfg.AgentAPI.AgentID,
		Action:    Action(decision.Action),
		RequestID: fmt.Sprintf("api-%s-%d-%06d", h.cfg.AgentAPI.AgentID, h.started.UnixNano(), h.decision),
	}
	switch decision.Action {
	case "pay":
		if decision.Target == h.cfg.AgentAPI.AgentID {
			return "", fmt.Errorf("%w: self payment is not allowed", errInvalidDecision)
		}

		if _, err := h.compiler.registry.Active(decision.Target); err != nil {
			return "", fmt.Errorf("%w: %w", errInvalidDecision, err)
		}

		amount, err := strconv.ParseUint(decision.Amount, 10, 64)
		if err != nil || amount == 0 {
			return "", fmt.Errorf("%w: pay amount must be a positive uint64 decimal string", errInvalidDecision)
		}

		var available *big.Int

		for _, agent := range obs.Agents {
			if agent.AgentID == h.cfg.AgentAPI.AgentID {
				available, _ = new(big.Int).SetString(agent.AvailableBalance, 10)
			}
		}

		if available == nil || available.Cmp(new(big.Int).SetUint64(amount)) < 0 {
			return "", fmt.Errorf("%w: insufficient available balance", errInvalidDecision)
		}

		record.Target = decision.Target
		record.Amount = amount
	case "leave":
		if decision.Target != "" || decision.Amount != "" {
			return "", fmt.Errorf("%w: leave cannot include target or amount", errInvalidDecision)
		}
	default:
		return "", fmt.Errorf("%w: unsupported action %q", errInvalidDecision, decision.Action)
	}

	hash, err := h.submitRecord(ctx, record, now)
	if err == nil {
		h.apiPending = hash
	}

	return hash, err
}

func (h *HybridRuntime) finishAudit(result string, now time.Time) {
	if h.current.Started.IsZero() {
		return
	}

	if h.current.Finished.IsZero() {
		h.current.Finished = now
		h.current.DurationMS = now.Sub(h.current.Started).Milliseconds()
		h.current.BlocksGranted = h.budget.Granted - h.grantBaseline
		h.current.BlocksCommitted = h.budget.Committed - h.commitBaseline
		h.logThinkingFinished(errors.New(result))
	}

	h.current.Result = result
	h.logDecisionResult()
	h.audits = append(h.audits, h.current)
	h.current = DecisionAudit{}
	h.nextDecision = now.Add(time.Duration(h.cfg.AgentAPI.DecisionIntervalSeconds) * time.Second)
	slog.Info(
		"agent decision finished",
		"decision",
		h.decision,
		"result",
		result,
		"blocks_granted",
		h.budget.Granted-h.grantBaseline,
	)
}

func (h *HybridRuntime) persist() error {
	dir := filepath.Join(h.cfg.Base.ResultDir, roundDirName(1))
	if err := h.compiler.WriteResult(
		dir,
		Result{Transactions: h.compiler.txs, Metrics: h.compiler.metrics},
	); err != nil {
		return err
	}

	summary := struct {
		AgentID               string          `json:"agent_id"`
		Model                 string          `json:"model"`
		Phase                 string          `json:"phase"`
		TraceActionsReplaced  int             `json:"trace_actions_replaced"`
		TraceActionsRemaining int             `json:"trace_actions_remaining"`
		PendingTransactions   int             `json:"pending_transactions"`
		BlocksCommitted       uint64          `json:"blocks_committed"`
		Latest                Observation     `json:"latest_snapshot"`
		Decisions             []DecisionAudit `json:"decisions"`
	}{h.cfg.AgentAPI.AgentID, h.cfg.AgentAPI.Model, h.phase, h.skipped, len(h.records) - h.cursor, len(h.pending), h.budget.Committed, h.latest, h.audits}

	data, err := json.MarshalIndent(summary, "", "  ")
	if err != nil {
		return fmt.Errorf("encode hybrid summary: %w", err)
	}

	if err := os.WriteFile(
		filepath.Join(h.cfg.Base.ResultDir, HybridSummaryFile),
		append(data, '\n'),
		0o600,
	); err != nil {
		return fmt.Errorf("write hybrid summary: %w", err)
	}

	return nil
}
func (h *HybridRuntime) ShouldStop() bool { return h.done }
func (h *HybridRuntime) Close() error {
	if h.cancel != nil {
		h.cancel()
	}

	h.finishAudit("runtime stopped before decision validation completed", time.Now())

	persistErr := h.persist()
	if err := h.events.Close(); err != nil && persistErr == nil {
		persistErr = fmt.Errorf("close action trace: %w", err)
	}

	if err := h.thinkingFile.Close(); err != nil {
		persistErr = errors.Join(persistErr, fmt.Errorf("close agent thinking log: %w", err))
	}

	return persistErr
}

func (h *HybridRuntime) updateLifecycle(now time.Time) error {
	end := h.started.Add(time.Duration(h.cfg.AgentAPI.RunDurationSeconds) * time.Second)
	if !now.Before(end) && !h.closing {
		h.closing = true
		if h.cancel != nil {
			h.cancel()
		}

		if h.phase != phaseIdle {
			h.finishAudit("experiment duration reached", now)
			h.phase = phaseIdle
		}

		h.budget.Release()
	}

	if h.closing && now.After(end.Add(time.Duration(h.cfg.AgentAPI.DrainTimeoutSeconds)*time.Second)) {
		return fmt.Errorf("hybrid drain timeout: %d transactions unconfirmed", len(h.pending))
	}

	return nil
}

func (h *HybridRuntime) pollModel(now time.Time) error {
	if h.phase == phaseThinking {
		select {
		case outcome := <-h.resultCh:
			if h.cancel != nil {
				h.cancel()
				h.cancel = nil
			}

			h.outcome = outcome
			h.current.Finished = now
			h.current.DurationMS = now.Sub(h.current.Started).Milliseconds()
			h.current.BlocksGranted = h.budget.Granted - h.grantBaseline
			h.current.BlocksCommitted = h.budget.Committed - h.commitBaseline

			h.current.Reply = outcome.Reply
			h.logThinkingFinished(outcome.Err)

			if outcome.Err != nil {
				h.finishAudit(outcome.Err.Error(), now)
				h.phase = phaseIdle
				h.budget.Release()

				if err := h.persist(); err != nil {
					return err
				}
			} else {
				h.phase = phaseValidationQuiesce
				h.phaseStarted = now
				h.budget.Hold()
			}
		default:
		}
	}

	return nil
}

func (h *HybridRuntime) advanceSnapshots(ctx context.Context, now time.Time) error {
	if (h.phase == phaseQuiesce || h.phase == phaseValidationQuiesce) && h.budget.Drained() {
		phase := phaseSnapshot
		if h.phase == phaseValidationQuiesce {
			phase = phaseValidationSnapshot
		}

		if err := h.requestSnapshot(ctx, phase, now); err != nil {
			return err
		}
	}

	if (h.phase == phaseSnapshot || h.phase == phaseValidationSnapshot || h.phase == phaseFinalSnapshot) &&
		len(h.snapshots) == int(h.chainCfg.ShardNum) {
		obs, err := h.buildObservation(now)
		if err != nil {
			return err
		}

		h.latest = obs
		switch h.phase {
		case phaseSnapshot:
			h.startDecision(obs, now)
		case phaseValidationSnapshot:
			hash, err := h.applyDecision(ctx, h.outcome.Reply.Decision, obs, now)
			if err != nil && !errors.Is(err, errInvalidDecision) {
				return err
			}

			result := "accepted"
			if err != nil {
				result = "rejected: " + err.Error()
			}

			h.current.TransactionHash = hash
			h.finishAudit(result, now)
			h.phase = phaseIdle
			h.budget.Release()

		case phaseFinalSnapshot:
			h.phase = phaseFinished
			h.done = true
		}

		if err := h.persist(); err != nil {
			return err
		}
	}

	return nil
}
