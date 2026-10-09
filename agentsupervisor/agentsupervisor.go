package agentsupervisor

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math/big"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/ethereum/go-ethereum/accounts/abi"

	"github.com/HuangLab-SYSU/block-emulator-x/pkg/core/account"
	"github.com/HuangLab-SYSU/block-emulator-x/pkg/core/transaction"
	"github.com/HuangLab-SYSU/block-emulator-x/pkg/utils"
)

const contractABI = `[
 {"type":"function","name":"register","inputs":[{"name":"didHash","type":"bytes32"},{"name":"docHash","type":"bytes32"}]},
 {"type":"function","name":"revoke","inputs":[{"name":"didHash","type":"bytes32"}]}
]`

// Output file names inside a round directory (and, for the registry, the result root).
const (
	PlanFileName     = "agent_transactions.jsonl"
	MetricsFileName  = "Agent_Events.csv"
	RegistryFileName = "agent_registry.json"
	// ActionTxMapFileName maps every trace action to the transactions it
	// compiled into, joining payment intents (request_id) with on-chain hashes.
	ActionTxMapFileName = "agent_action_txs.jsonl"
)

type MetricEvent struct {
	Kind      string
	TS        int64
	RequestID string
	Value     uint64
}

type Result struct {
	Transactions []transaction.Transaction
	Metrics      []MetricEvent
}

type sellQuote struct {
	SellerAgentID string
	SellerAddr    account.Address
	ComputeID     string
	UnitPrice     *big.Int
	Quantity      uint64
	RequestID     string
	TS            int64
}

// ActionTxLink is one trace action and the transactions it compiled into.
type ActionTxLink struct {
	Seq        int      `json:"seq"`
	Action     Action   `json:"action"`
	AgentID    string   `json:"agent_id"`
	Target     string   `json:"target"`
	Amount     uint64   `json:"amount"`
	ComputeID  string   `json:"compute_id,omitempty"`
	UnitPrice  string   `json:"unit_price,omitempty"`
	Quantity   uint64   `json:"quantity,omitempty"`
	TS         int64    `json:"ts"`
	RequestID  string   `json:"request_id"`
	ParamsHash string   `json:"params_hash"`
	TxHashes   []string `json:"tx_hashes"`
}

// AgentSupervisor compiles lifecycle and payment actions into existing transactions.
type AgentSupervisor struct {
	cfg          Config
	abi          abi.ABI
	nonces       map[account.Address]uint64
	registry     *Registry
	registryPath string
	quotes       map[string]sellQuote
	metrics      []MetricEvent
	txs          []transaction.Transaction
	links        []ActionTxLink
	curLink      int
}

func NewAgentSupervisor(cfg Config) (*AgentSupervisor, error) {
	parsed, err := abi.JSON(strings.NewReader(contractABI))
	if err != nil {
		return nil, fmt.Errorf("parse built-in contract ABI: %w", err)
	}

	// The registry lives at the result root so agent identities survive across
	// simulation rounds; per-round outputs go to their own directories.
	registryPath := filepath.Join(cfg.Base.ResultDir, RegistryFileName)

	registry, err := LoadRegistry(registryPath, cfg.Experiment.Seed)
	if err != nil {
		return nil, err
	}

	return &AgentSupervisor{
		cfg:          cfg,
		abi:          parsed,
		nonces:       make(map[account.Address]uint64),
		registry:     registry,
		registryPath: registryPath,
		quotes:       make(map[string]sellQuote),
	}, nil
}

func (s *AgentSupervisor) Process(records []Record) (Result, error) {
	for _, record := range records {
		if err := s.process(record); err != nil {
			return Result{}, fmt.Errorf("process trace line %d: %w", record.Seq, err)
		}
	}

	return Result{Transactions: s.txs, Metrics: s.metrics}, nil
}

func (s *AgentSupervisor) process(record Record) error {
	// Every transaction compiled below is attributed to this action's link.
	s.curLink = len(s.links)
	s.links = append(s.links, ActionTxLink{
		Seq:        record.Seq,
		Action:     record.Action,
		AgentID:    record.AgentID,
		Target:     record.Target,
		Amount:     record.Amount,
		ComputeID:  record.ComputeID,
		UnitPrice:  record.UnitPrice,
		Quantity:   record.Quantity,
		TS:         record.TS,
		RequestID:  record.RequestID,
		ParamsHash: record.ParamsHash,
	})

	switch record.Action {
	case ActionJoin:
		return s.processJoin(record)
	case ActionLeave:
		return s.processLeave(record)
	case ActionPay:
		return s.processPay(record)
	case ActionBuy:
		return s.processBuy(record)
	case ActionSell:
		return s.processSell(record)
	case ActionRawTx:
		return s.processRawTx(record)
	default:
		return fmt.Errorf("unsupported action %q", record.Action)
	}
}

func (s *AgentSupervisor) processJoin(record Record) error {
	agent, changed, err := s.registry.Join(record.AgentID)
	if err != nil {
		return err
	}

	if !changed {
		return nil
	}

	return s.processIdentity(record, agent, "register")
}

func (s *AgentSupervisor) processLeave(record Record) error {
	agent, err := s.registry.Leave(record.AgentID)
	if err != nil {
		return err
	}

	return s.processIdentity(record, agent, "revoke")
}

func (s *AgentSupervisor) processPay(record Record) error {
	fromAgent, err := s.registry.Active(record.AgentID)
	if err != nil {
		return err
	}

	toAgent, err := s.registry.Active(record.Target)
	if err != nil {
		return err
	}

	from, err := didAddress(fromAgent.DID)
	if err != nil {
		return err
	}

	to, err := didAddress(toAgent.DID)
	if err != nil {
		return err
	}

	s.appendTx(from, to, record.Amount, nil, record.TS)
	s.linkLastTxTo(s.curLink)
	s.metric("pay_onchain", record)

	return nil
}

func (s *AgentSupervisor) processSell(record Record) error {
	seller, err := s.registry.Active(record.AgentID)
	if err != nil {
		return err
	}

	sellerAddr, err := didAddress(seller.DID)
	if err != nil {
		return err
	}

	unitPrice, err := parseRWAUnitPrice(record.UnitPrice)
	if err != nil {
		return err
	}

	quote := sellQuote{
		SellerAgentID: record.AgentID,
		SellerAddr:    sellerAddr,
		ComputeID:     record.ComputeID,
		UnitPrice:     new(big.Int).Set(unitPrice),
		Quantity:      record.Quantity,
		RequestID:     record.RequestID,
		TS:            record.TS,
	}
	s.quotes[quoteKey(record.AgentID, record.ComputeID)] = quote

	tx := s.appendBigTx(sellerAddr, account.EmptyAccountAddr, big.NewInt(0), nil, record.TS)
	tx.RWATxOpt = transaction.RWATxOpt{
		RWAAction: transaction.RWAActionSell,
		AgentID:   record.AgentID,
		RequestID: record.RequestID,
		ComputeID: record.ComputeID,
		UnitPrice: new(big.Int).Set(unitPrice),
		Quantity:  record.Quantity,
	}

	s.linkLastTxTo(s.curLink)
	s.metric("rwa_sell_onchain", record)

	return nil
}

func (s *AgentSupervisor) processBuy(record Record) error {
	buyer, err := s.registry.Active(record.AgentID)
	if err != nil {
		return err
	}

	if _, err := s.registry.Active(record.Target); err != nil {
		return err
	}

	buyerAddr, err := didAddress(buyer.DID)
	if err != nil {
		return err
	}

	quote, ok := s.quotes[quoteKey(record.Target, record.ComputeID)]
	if !ok {
		return fmt.Errorf("missing sell quote for seller %q compute_id %q", record.Target, record.ComputeID)
	}

	value := new(big.Int).Mul(quote.UnitPrice, new(big.Int).SetUint64(record.Quantity))
	tx := s.appendBigTx(buyerAddr, quote.SellerAddr, value, nil, record.TS)
	tx.RWATxOpt = transaction.RWATxOpt{
		RWAAction: transaction.RWAActionBuy,
		AgentID:   record.AgentID,
		TargetID:  record.Target,
		RequestID: record.RequestID,
		ComputeID: record.ComputeID,
		Quantity:  record.Quantity,
	}

	s.linkLastTxTo(s.curLink)
	s.metric("rwa_buy_onchain", record)

	return nil
}

// processRawTx compiles a plain transfer line like any other transaction:
// only sender, recipient and value come from the trace, while the nonce is
// taken from the shared per-sender counter and data stays empty.
func (s *AgentSupervisor) processRawTx(record Record) error {
	spec := record.RawTx

	from, err := rawTxAddr(spec.Sender, "sender")
	if err != nil {
		return err
	}

	to, err := rawTxAddr(spec.Recipient, "recipient")
	if err != nil {
		return err
	}

	value, ok := new(big.Int).SetString(spec.Value, 10)
	if !ok {
		return fmt.Errorf("parse raw tx value %q", spec.Value)
	}

	if !value.IsUint64() {
		return fmt.Errorf("raw tx value %s exceeds uint64", value)
	}

	s.appendTx(from, to, value.Uint64(), nil, record.TS)
	s.linkLastTxTo(s.curLink)
	s.metrics = append(s.metrics, MetricEvent{Kind: "raw_tx", TS: record.TS, Value: value.Uint64()})

	return nil
}

// rawTxAddr parses a plain-transfer address field; utils.Hex2Addr zero-pads
// short input, so the 20-byte length is checked explicitly.
func rawTxAddr(hexAddr, field string) (account.Address, error) {
	b, err := utils.Hex2Bytes(hexAddr)
	if err != nil {
		return account.Address{}, fmt.Errorf("parse raw tx %s: %w", field, err)
	}

	if len(b) != 20 {
		return account.Address{}, fmt.Errorf("raw tx %s must be a 20-byte address, got %d bytes", field, len(b))
	}

	var addr account.Address
	copy(addr[:], b)

	return addr, nil
}

func (s *AgentSupervisor) processIdentity(record Record, agent Agent, operation string) error {
	from, err := didAddress(agent.DID)
	if err != nil {
		return err
	}

	didHash := sha256.Sum256([]byte(agent.DID))

	var data []byte

	switch operation {
	case "register":
		docHash := sha256.Sum256([]byte(record.ParamsHash))
		data, err = s.abi.Pack("register", didHash, docHash)
	case "revoke":
		data, err = s.abi.Pack("revoke", didHash)
	default:
		return fmt.Errorf("unknown DID operation %q", operation)
	}

	if err != nil {
		return fmt.Errorf("pack DID operation: %w", err)
	}

	if err := s.appendContractTx(from, s.cfg.Protocols.Identity.ContractAddress, data, record.TS); err != nil {
		return err
	}

	s.linkLastTxTo(s.curLink)
	s.metric("did_"+operation, record)

	return nil
}

func (s *AgentSupervisor) appendTx(from, to account.Address, amount uint64, data []byte, ts int64) {
	s.appendBigTx(from, to, new(big.Int).SetUint64(amount), data, ts)
}

func (s *AgentSupervisor) appendBigTx(from, to account.Address, amount *big.Int, data []byte, ts int64) *transaction.Transaction {
	nonce := s.nonces[from]
	s.nonces[from]++
	tx := transaction.NewTransaction(from, to, new(big.Int).Set(amount), big.NewInt(0), nonce, time.UnixMilli(ts))
	tx.Data = data
	s.txs = append(s.txs, *tx)

	return &s.txs[len(s.txs)-1]
}

// linkLastTxTo records the hash of the most recently appended transaction in
// the given action's link.
func (s *AgentSupervisor) linkLastTxTo(linkIdx int) {
	hash, err := s.txs[len(s.txs)-1].Hash()
	if err != nil {
		// Hashing a well-formed transaction does not fail; the plan writer
		// surfaces such an error for every transaction anyway.
		return
	}

	s.links[linkIdx].TxHashes = append(s.links[linkIdx].TxHashes, hex.EncodeToString(hash))
}

func (s *AgentSupervisor) appendContractTx(from account.Address, address string, data []byte, ts int64) error {
	to, err := utils.Hex2Addr(address)
	if err != nil {
		return fmt.Errorf("parse contract address: %w", err)
	}

	s.appendTx(from, to, 0, data, ts)

	return nil
}

func quoteKey(agentID, computeID string) string {
	return agentID + "\x00" + computeID
}

func parseRWAUnitPrice(raw string) (*big.Int, error) {
	price, ok := new(big.Int).SetString(raw, 10)
	if !ok || price.Sign() < 0 {
		return nil, fmt.Errorf("unit_price must be a non-negative integer")
	}

	return price, nil
}

func (s *AgentSupervisor) metric(kind string, record Record) {
	s.metrics = append(
		s.metrics,
		MetricEvent{Kind: kind, TS: record.TS, RequestID: record.RequestID, Value: record.Amount},
	)
}

// WriteResult persists the shared agent registry and writes the round's
// transaction plan, metric events and the action-to-transaction map into dir.
func (s *AgentSupervisor) WriteResult(dir string, result Result) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create result directory: %w", err)
	}

	if err := s.registry.Write(s.registryPath); err != nil {
		return err
	}

	if err := writeActionTxMap(dir, s.links); err != nil {
		return err
	}

	return writeResult(dir, result)
}

func didAddress(did string) (account.Address, error) {
	const prefix = "did:broker:"
	if !strings.HasPrefix(did, prefix) {
		return account.Address{}, fmt.Errorf("invalid DID %q", did)
	}

	hexAddr := strings.TrimPrefix(did, prefix)
	if len(strings.TrimPrefix(hexAddr, "0x")) != 40 {
		return account.Address{}, fmt.Errorf("DID must carry a 20-byte address")
	}

	addr, err := utils.Hex2Addr(hexAddr)
	if err != nil {
		return account.Address{}, fmt.Errorf("parse DID address: %w", err)
	}

	return addr, nil
}

// writeActionTxMap persists the action-to-transaction map: one JSON object
// per trace action, carrying the payment intent (request_id) and every
// transaction hash the action compiled into.
func writeActionTxMap(dir string, links []ActionTxLink) error {
	f, err := os.Create(filepath.Join(dir, ActionTxMapFileName))
	if err != nil {
		return fmt.Errorf("create action tx map: %w", err)
	}

	defer func() { _ = f.Close() }()

	w := bufio.NewWriter(f)

	for _, link := range links {
		b, err := json.Marshal(link)
		if err != nil {
			return fmt.Errorf("encode action tx map entry: %w", err)
		}

		if _, err := w.Write(append(b, '\n')); err != nil {
			return fmt.Errorf("write action tx map entry: %w", err)
		}
	}

	return w.Flush()
}

type planLine struct {
	Hash      string `json:"hash"`
	Sender    string `json:"sender"`
	Recipient string `json:"recipient"`
	Value     string `json:"value"`
	Nonce     uint64 `json:"nonce"`
	Data      string `json:"data"`
	RWAAction string `json:"rwa_action,omitempty"`
	AgentID   string `json:"agent_id,omitempty"`
	TargetID  string `json:"target_id,omitempty"`
	RequestID string `json:"request_id,omitempty"`
	ComputeID string `json:"compute_id,omitempty"`
	UnitPrice string `json:"unit_price,omitempty"`
	Quantity  uint64 `json:"quantity,omitempty"`
}

func makePlanLine(tx transaction.Transaction) (planLine, error) {
	hash, err := tx.Hash()
	if err != nil {
		return planLine{}, fmt.Errorf("hash planned transaction: %w", err)
	}

	line := planLine{
		Hash:      fmt.Sprintf("%x", hash),
		Sender:    fmt.Sprintf("0x%x", tx.Sender),
		Recipient: fmt.Sprintf("0x%x", tx.Recipient),
		Value:     tx.Value.String(),
		Nonce:     tx.Nonce,
		Data:      fmt.Sprintf("0x%x", tx.Data),
	}

	if tx.IsRWATx() {
		line.RWAAction = tx.RWAAction
		line.AgentID = tx.AgentID
		line.TargetID = tx.TargetID
		line.RequestID = tx.RequestID
		line.ComputeID = tx.ComputeID
		line.Quantity = tx.Quantity
		if tx.UnitPrice != nil {
			line.UnitPrice = tx.UnitPrice.String()
		}
		if tx.IsRWASell() {
			line.Recipient = ""
			line.Value = ""
		}
	}

	return line, nil
}

func writeResult(dir string, result Result) error {
	plan, err := os.Create(filepath.Join(dir, PlanFileName))
	if err != nil {
		return fmt.Errorf("create transaction plan: %w", err)
	}

	defer func() { _ = plan.Close() }()

	planWriter := bufio.NewWriter(plan)
	for _, tx := range result.Transactions {
		line, err := makePlanLine(tx)
		if err != nil {
			return err
		}

		b, err := json.Marshal(line)
		if err != nil {
			return fmt.Errorf("encode transaction plan: %w", err)
		}

		if _, err := planWriter.Write(append(b, '\n')); err != nil {
			return fmt.Errorf("write transaction plan: %w", err)
		}
	}

	if err := planWriter.Flush(); err != nil {
		return fmt.Errorf("flush transaction plan: %w", err)
	}

	metrics, err := os.Create(filepath.Join(dir, MetricsFileName))
	if err != nil {
		return fmt.Errorf("create metrics: %w", err)
	}

	defer func() { _ = metrics.Close() }()

	if _, err := fmt.Fprintln(metrics, "kind,ts,request_id,value"); err != nil {
		return fmt.Errorf("write metrics header: %w", err)
	}

	for _, event := range result.Metrics {
		if _, err := fmt.Fprintf(
			metrics,
			"%s,%d,%s,%d\n",
			event.Kind,
			event.TS,
			event.RequestID,
			event.Value,
		); err != nil {
			return fmt.Errorf("write metric: %w", err)
		}
	}

	return nil
}
