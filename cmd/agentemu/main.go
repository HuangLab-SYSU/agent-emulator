package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/HuangLab-SYSU/block-emulator-x/agentemu"
)

func main() {
	configPath := flag.String("config", "agentEmuConfig.yaml", "path to AgentEmulator YAML config")

	flag.Parse()

	// Ctrl-C must tear down any running cluster processes.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	cfg, err := agentemu.LoadConfig(*configPath)
	if err != nil {
		slog.Error("load config", "err", err)
		os.Exit(1)
	}

	records, err := agentemu.LoadTrace(cfg.Experiment.Trace)
	if err != nil {
		slog.Error("load trace", "err", err)
		os.Exit(1)
	}

	var chain *agentemu.ChainRunner
	if cfg.Chain.Enabled {
		chain = &agentemu.ChainRunner{
			ModuleRoot:    cfg.Base.ModuleRoot,
			BaseConfig:    cfg.Base.BlockEmulatorConfig,
			WorkRoot:      filepath.Join(cfg.Base.ResultDir, "chain"),
			RunTimeout:    time.Duration(cfg.Chain.RunTimeoutSeconds) * time.Second,
			NodeExitGrace: time.Duration(cfg.Chain.NodeExitGraceSeconds) * time.Second,
		}
		if err := chain.Build(ctx); err != nil {
			slog.Error("build blockemulator binaries", "err", err)
			os.Exit(1)
		}
	}

	// After the plan is produced, the chain (when enabled) replays it on a
	// freshly launched BlockEmulator-X cluster; the loop is reserved for
	// multi-round feedback via the AgentAPI hook.
	runner := &agentemu.Runner{Cfg: cfg, Records: records, Chain: chain}

	rounds, err := runner.Run(ctx)
	for _, rr := range rounds {
		slog.Info("round finished",
			"round", rr.Round,
			"records", rr.RecordCount,
			"txs", rr.TxCount,
			"plan", rr.PlanPath,
			"chain_results", rr.ChainResultDir,
		)
	}

	if err != nil {
		slog.Error("simulation run", "err", err)
		os.Exit(1)
	}

	fmt.Printf(
		"agentemu finished %d round(s); summary at %s\n",
		len(rounds),
		filepath.Join(cfg.Base.ResultDir, agentemu.RoundSummaryFile),
	)
}
