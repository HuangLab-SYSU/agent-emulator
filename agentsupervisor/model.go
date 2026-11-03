package agentsupervisor

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/HuangLab-SYSU/block-emulator-x/config"
	"github.com/HuangLab-SYSU/block-emulator-x/pkg/message"
)

// ModelConfig binds one trace identity to a configurable model endpoint.
// Additional wire protocols implement DecisionModel without changing the runtime.
type ModelConfig struct {
	InitialBalance          string  `yaml:"initial_balance,omitempty"`
	Enabled                 bool    `yaml:"enabled"`
	AgentID                 string  `yaml:"agent_id"`
	Adapter                 string  `yaml:"adapter"`
	BaseURL                 string  `yaml:"base_url"`
	Model                   string  `yaml:"model"`
	APIKeyEnv               string  `yaml:"api_key_env,omitempty"`
	APIKeyFile              string  `yaml:"api_key_file,omitempty"`
	Goal                    string  `yaml:"goal"`
	MaxBlocksDuringDecision *uint64 `yaml:"max_blocks_during_decision"`
	DecisionTimeoutSeconds  int     `yaml:"decision_timeout_seconds"`
	DecisionIntervalSeconds int     `yaml:"decision_interval_seconds"`
	RunDurationSeconds      int     `yaml:"run_duration_seconds"`
	DrainTimeoutSeconds     int     `yaml:"drain_timeout_seconds"`
	TraceTimeScale          float64 `yaml:"trace_time_scale"`
}

func (c *ModelConfig) normalize(chainEnabled bool) error {
	if !c.Enabled {
		return nil
	}

	if !chainEnabled {
		return fmt.Errorf("agent_api requires chain.enabled=true")
	}

	if c.AgentID == "" || c.BaseURL == "" || c.Model == "" {
		return fmt.Errorf("agent_api.agent_id, base_url and model are required")
	}

	if c.InitialBalance != "" {
		if _, err := config.ParseInitialBalance(c.InitialBalance); err != nil {
			return fmt.Errorf("agent_api.initial_balance: %w", err)
		}
	}

	if c.Adapter == "" {
		c.Adapter = "chat-completions"
	}

	if c.Adapter != "chat-completions" {
		return fmt.Errorf("unsupported agent_api.adapter %q", c.Adapter)
	}

	if c.APIKeyEnv != "" && c.APIKeyFile != "" {
		return fmt.Errorf("configure only one of agent_api.api_key_env or api_key_file")
	}

	u, err := url.Parse(c.BaseURL)
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil {
		return fmt.Errorf("agent_api.base_url must be an HTTP(S) URL without embedded credentials")
	}

	if c.MaxBlocksDuringDecision == nil {
		n := uint64(5)
		c.MaxBlocksDuringDecision = &n
	}

	if c.DecisionTimeoutSeconds <= 0 {
		c.DecisionTimeoutSeconds = 60
	}

	if c.DecisionIntervalSeconds <= 0 {
		c.DecisionIntervalSeconds = 10
	}

	if c.RunDurationSeconds <= 0 {
		c.RunDurationSeconds = 120
	}

	if c.DrainTimeoutSeconds <= 0 {
		c.DrainTimeoutSeconds = 60
	}

	if c.TraceTimeScale < 0 {
		return fmt.Errorf("agent_api.trace_time_scale must be non-negative")
	}

	if c.TraceTimeScale == 0 {
		c.TraceTimeScale = 1
	}

	return nil
}

type ObservedAgent struct {
	AgentID          string `json:"agent_id"`
	Active           bool   `json:"active"`
	Balance          string `json:"balance"`
	AvailableBalance string `json:"available_balance"`
}

type Observation struct {
	ElapsedMS               int64                           `json:"elapsed_ms"`
	SettledPaymentsByTarget map[string]string               `json:"settled_payments_by_target"`
	LatestBlocks            map[int64]message.SnapshotBlock `json:"latest_blocks"`
	AgentID                 string                          `json:"agent_id"`
	Decision                uint64                          `json:"decision"`
	ObservedAt              time.Time                       `json:"observed_at"`
	Heights                 map[int64]uint64                `json:"shard_heights"`
	Agents                  []ObservedAgent                 `json:"agents"`
	PendingTransactions     int                             `json:"pending_transactions"`
	PreviousResult          string                          `json:"previous_result,omitempty"`
}

type Decision struct {
	Action string `json:"action"`
	Target string `json:"target,omitempty"`
	Amount string `json:"amount,omitempty"`
}

type ModelReply struct {
	Decision Decision        `json:"decision"`
	Raw      json.RawMessage `json:"raw_response,omitempty"`
	Attempts int             `json:"attempts"`
}

type DecisionModel interface {
	Decide(context.Context, Observation) (ModelReply, error)
}

type ChatModel struct {
	Config ModelConfig
	Client *http.Client
}

func (m *ChatModel) Decide(ctx context.Context, observation Observation) (ModelReply, error) {
	prompt := "You control only agent " + m.Config.AgentID + ". Goal: " + m.Config.Goal + ". Return exactly one JSON object: {\"action\":\"pay\",\"target\":\"agent-id\",\"amount\":\"positive decimal integer\"}, or {\"action\":\"wait\"}, or {\"action\":\"leave\"}. Pay only an active other agent and never exceed your available_balance. Do not return reasoning, markdown, IDs, timestamps or transactions."

	observationJSON, err := json.Marshal(observation)
	if err != nil {
		return ModelReply{}, fmt.Errorf("encode observation: %w", err)
	}

	requestBody, err := json.Marshal(
		map[string]any{
			"model": m.Config.Model,
			"messages": []map[string]string{
				{"role": "system", "content": prompt},
				{"role": "user", "content": string(observationJSON)},
			},
		},
	)
	if err != nil {
		return ModelReply{}, fmt.Errorf("encode model request: %w", err)
	}

	client := m.Client
	if client == nil {
		client = &http.Client{Timeout: time.Duration(m.Config.DecisionTimeoutSeconds) * time.Second}
	}

	key, err := m.Config.loadAPIKey()
	if err != nil {
		return ModelReply{}, err
	}

	endpoint := strings.TrimRight(m.Config.BaseURL, "/") + "/chat/completions"

	var reply ModelReply
	for attempt := 1; attempt <= 3; attempt++ {
		reply.Attempts = attempt
		if attempt > 1 {
			timer := time.NewTimer(time.Duration(attempt-1) * time.Second)
			select {
			case <-ctx.Done():
				timer.Stop()
				return reply, fmt.Errorf("model retry: %w", ctx.Err())
			case <-timer.C:
			}
		}

		req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(requestBody))
		if err != nil {
			return reply, fmt.Errorf("create model request: %w", err)
		}

		req.Header.Set("Content-Type", "application/json")

		if key != "" {
			req.Header.Set("Authorization", "Bearer "+key)
		}

		resp, err := client.Do(req)
		if err != nil {
			if ctx.Err() != nil {
				return reply, fmt.Errorf("model call: %w", ctx.Err())
			}

			if attempt < 3 {
				continue
			}

			return reply, fmt.Errorf("model transport failed after %d attempts", attempt)
		}

		body, readErr := io.ReadAll(io.LimitReader(resp.Body, (1<<20)+1))
		closeErr := resp.Body.Close()

		if readErr != nil {
			return reply, fmt.Errorf("read model response: %w", readErr)
		}

		if closeErr != nil {
			return reply, fmt.Errorf("close model response: %w", closeErr)
		}

		if len(body) > 1<<20 {
			return reply, fmt.Errorf("model response exceeds 1 MiB")
		}

		if resp.StatusCode == 429 || resp.StatusCode >= 500 {
			if attempt < 3 {
				continue
			}
		}

		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			return reply, fmt.Errorf("model HTTP status %d", resp.StatusCode)
		}

		if !json.Valid(body) {
			return reply, fmt.Errorf("model returned invalid JSON envelope")
		}

		reply.Raw = append(json.RawMessage(nil), body...)

		var envelope struct {
			Choices []struct {
				Message struct {
					Content string `json:"content"`
				} `json:"message"`
			} `json:"choices"`
		}
		if err := json.Unmarshal(body, &envelope); err != nil {
			return reply, fmt.Errorf("decode model response: %w", err)
		}

		if len(envelope.Choices) == 0 {
			return reply, fmt.Errorf("model returned no choices")
		}

		decoder := json.NewDecoder(strings.NewReader(envelope.Choices[0].Message.Content))
		decoder.DisallowUnknownFields()

		if err := decoder.Decode(&reply.Decision); err != nil {
			return reply, fmt.Errorf("decode decision: %w", err)
		}

		var extra any
		if err := decoder.Decode(&extra); err != io.EOF {
			return reply, fmt.Errorf("decision must contain exactly one JSON object")
		}

		switch reply.Decision.Action {
		case "pay", "wait", "leave":
		default:
			return reply, fmt.Errorf("unsupported decision action %q", reply.Decision.Action)
		}

		return reply, nil
	}

	return reply, fmt.Errorf("model retries exhausted")
}

// Read once per decision, allowing local key rotation without copying secrets
// into run configuration snapshots or model results.
func (c ModelConfig) loadAPIKey() (string, error) {
	if c.APIKeyEnv != "" && c.APIKeyFile != "" {
		return "", fmt.Errorf("configure only one of agent_api.api_key_env or api_key_file")
	}

	var key string

	switch {
	case c.APIKeyFile != "":
		f, err := os.Open(c.APIKeyFile)
		if err != nil {
			return "", fmt.Errorf("open API key file: %w", err)
		}

		data, readErr := io.ReadAll(io.LimitReader(f, 16385))
		closeErr := f.Close()

		if readErr != nil {
			return "", fmt.Errorf("read API key file: %w", readErr)
		}

		if closeErr != nil {
			return "", fmt.Errorf("close API key file: %w", closeErr)
		}

		if len(data) > 16384 {
			return "", fmt.Errorf("API key file exceeds 16 KiB")
		}

		key = strings.TrimSpace(string(data))
		if key == "" {
			return "", fmt.Errorf("API key file is empty; paste only your API key into it")
		}
	case c.APIKeyEnv != "":
		key = strings.TrimSpace(os.Getenv(c.APIKeyEnv))
		if key == "" {
			return "", fmt.Errorf("model key environment variable %s is empty", c.APIKeyEnv)
		}
	default:
		return "", nil
	}

	if strings.ContainsAny(key, " \t\r\n") {
		return "", fmt.Errorf("API key must be a single token without internal whitespace")
	}

	return key, nil
}
