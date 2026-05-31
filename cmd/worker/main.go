package main

import (
	"log/slog"
	"os"

	"github.com/angoo/agentfoundry-worker/internal/config"
	"github.com/angoo/agentfoundry-worker/internal/memory"
	"github.com/angoo/agentfoundry-worker/internal/orchestrator"
	agentworker "github.com/angoo/agentfoundry-worker/internal/temporal"
)

func main() {
	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
	slog.SetDefault(logger)

	cfg, err := config.LoadSystem("worker.yaml")
	if err != nil {
		slog.Warn("no worker.yaml found, using defaults", "error", err)
		cfg = config.DefaultSystem()
	}
	slog.Info("loaded system config",
		"temporal_host", cfg.Temporal.HostPort,
		"temporal_namespace", cfg.Temporal.Namespace,
		"orchestrator_url", cfg.Orchestrator.URL,
		"graphiti_url", cfg.GraphitiURL,
	)

	orchClient := orchestrator.NewClient(orchestrator.Config{
		URL:    cfg.Orchestrator.URL,
		APIKey: cfg.Orchestrator.APIKey,
	})

	var memClient *memory.Client
	if cfg.GraphitiURL != "" {
		memClient = memory.NewClient(cfg.GraphitiURL)
		slog.Info("graphiti memory client configured", "url", cfg.GraphitiURL)
	}

	w, err := agentworker.NewWorker(cfg.Temporal, orchClient, memClient)
	if err != nil {
		slog.Error("failed to create Temporal worker", "error", err)
		os.Exit(1)
	}

	slog.Info("starting Temporal worker",
		"task_queue", agentworker.TaskQueue,
		"temporal", cfg.Temporal.HostPort,
		"namespace", cfg.Temporal.Namespace,
	)

	if err := w.Start(); err != nil {
		slog.Error("worker error", "error", err)
		os.Exit(1)
	}

	slog.Info("worker stopped")
}
