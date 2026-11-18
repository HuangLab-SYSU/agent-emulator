package agentsupervisor

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"time"
)

// The experiment process owns the model. The chain supervisor exchanges
// observations and decisions with it over an experiment-scoped loopback URL.
type decisionResponse struct {
	Reply ModelReply `json:"reply"`
	Error string     `json:"error,omitempty"`
}

func (s *AgentSupervisor) serveDecisions(ctx context.Context) (string, func(), error) {
	var token [32]byte
	if _, err := rand.Read(token[:]); err != nil {
		return "", nil, fmt.Errorf("generate decision endpoint token: %w", err)
	}

	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		return "", nil, fmt.Errorf("listen for chain decisions: %w", err)
	}

	path := "/" + hex.EncodeToString(token[:])
	model := &ChatModel{Config: s.cfg.AgentAPI}
	mux := http.NewServeMux()
	mux.HandleFunc("POST "+path, func(w http.ResponseWriter, r *http.Request) {
		var obs Observation
		if err := json.NewDecoder(r.Body).Decode(&obs); err != nil || obs.AgentID != s.cfg.AgentAPI.AgentID {
			http.Error(w, "invalid decision observation", http.StatusBadRequest)

			return
		}

		reply, err := model.Decide(r.Context(), obs)

		response := decisionResponse{Reply: reply}
		if err != nil {
			response.Error = err.Error()
		}

		w.Header().Set("Content-Type", "application/json")

		if err := json.NewEncoder(w).Encode(response); err != nil {
			slog.DebugContext(r.Context(), "write decision response", "err", err)
		}
	})

	server := &http.Server{
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
		BaseContext:       func(net.Listener) context.Context { return ctx },
	}

	go func() {
		if err := server.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
			slog.ErrorContext(ctx, "serve decisions", "err", err)
		}
	}()

	return "http://" + listener.Addr().String() + path, func() { _ = server.Close() }, nil
}

type supervisorDecisionClient struct {
	endpoint string
}

func (c supervisorDecisionClient) Decide(ctx context.Context, obs Observation) (ModelReply, error) {
	body, err := json.Marshal(obs)
	if err != nil {
		return ModelReply{}, fmt.Errorf("encode observation for agent supervisor: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint, bytes.NewReader(body))
	if err != nil {
		return ModelReply{}, fmt.Errorf("create agent supervisor request: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		// The local URL carries an experiment token. Omit it from logs.
		if ctx.Err() != nil {
			return ModelReply{}, fmt.Errorf("agent supervisor request: %w", ctx.Err())
		}

		return ModelReply{}, fmt.Errorf("agent supervisor request failed")
	}

	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return ModelReply{}, fmt.Errorf("agent supervisor returned HTTP %d", resp.StatusCode)
	}

	var response decisionResponse
	if err := json.NewDecoder(resp.Body).Decode(&response); err != nil {
		return ModelReply{}, fmt.Errorf("decode agent supervisor reply: %w", err)
	}

	if response.Error != "" {
		return response.Reply, errors.New(response.Error)
	}

	return response.Reply, nil
}
