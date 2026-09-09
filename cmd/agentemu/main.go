package main

import (
	"flag"
	"fmt"
	"log/slog"
	"os"

	"github.com/HuangLab-SYSU/block-emulator-x/agentemu"
)

func main() {
	configPath := flag.String("config", "agentEmuConfig.yaml", "path to AgentEmulator YAML config")
	flag.Parse()

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
	host, err := agentemu.NewHost(cfg)
	if err != nil {
		slog.Error("create host", "err", err)
		os.Exit(1)
	}
	result, err := host.Process(records)
	if err != nil {
		slog.Error("process trace", "err", err)
		os.Exit(1)
	}
	if err := host.WriteResult(result); err != nil {
		slog.Error("write result", "err", err)
		os.Exit(1)
	}
	fmt.Printf("agentemu wrote %d planned transactions and %d events to %s\n", len(result.Transactions), len(result.Metrics), cfg.Base.ResultDir)
}
