package config

import (
	"os"
	"strings"

	"gopkg.in/yaml.v3"
)

type SystemConfig struct {
	Temporal     TemporalConf     `yaml:"temporal"`
	Orchestrator OrchestratorConf `yaml:"orchestrator"`
	GraphitiURL  string           `yaml:"graphiti_url"`
}

type TemporalConf struct {
	HostPort  string `yaml:"host_port"`
	Namespace string `yaml:"namespace"`
	APIKey    string `yaml:"api_key"`
}

type OrchestratorConf struct {
	URL    string `yaml:"url"`
	APIKey string `yaml:"api_key"`
}

func DefaultSystem() *SystemConfig {
	hostPort := os.Getenv("TEMPORAL_HOST_PORT")
	if hostPort == "" {
		hostPort = "localhost:7233"
	}
	ns := os.Getenv("TEMPORAL_NAMESPACE")
	if ns == "" {
		ns = "default"
	}

	orchURL := os.Getenv("ORCHESTRATOR_URL")
	if orchURL == "" {
		orchURL = "http://localhost:3000"
	}

	return &SystemConfig{
		Temporal: TemporalConf{
			HostPort:  hostPort,
			Namespace: ns,
			APIKey:    os.Getenv("TEMPORAL_API_KEY"),
		},
		Orchestrator: OrchestratorConf{
			URL:    orchURL,
			APIKey: os.Getenv("ORCHESTRATOR_API_KEY"),
		},
		GraphitiURL: os.Getenv("GRAPHITI_URL"),
	}
}

func LoadSystem(path string) (*SystemConfig, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	cfg := DefaultSystem()
	if err := yaml.Unmarshal(data, cfg); err != nil {
		return nil, err
	}

	cfg.Temporal.HostPort = expandEnvVar(cfg.Temporal.HostPort)
	cfg.Temporal.Namespace = expandEnvVar(cfg.Temporal.Namespace)
	cfg.Temporal.APIKey = expandEnvVar(cfg.Temporal.APIKey)

	if cfg.Temporal.HostPort == "" {
		cfg.Temporal.HostPort = os.Getenv("TEMPORAL_HOST_PORT")
	}
	if cfg.Temporal.HostPort == "" {
		cfg.Temporal.HostPort = "localhost:7233"
	}
	if cfg.Temporal.Namespace == "" {
		cfg.Temporal.Namespace = "default"
	}
	if cfg.Temporal.APIKey == "" {
		cfg.Temporal.APIKey = os.Getenv("TEMPORAL_API_KEY")
	}

	cfg.Orchestrator.URL = expandEnvVar(cfg.Orchestrator.URL)
	cfg.Orchestrator.APIKey = expandEnvVar(cfg.Orchestrator.APIKey)

	if cfg.Orchestrator.URL == "" {
		cfg.Orchestrator.URL = os.Getenv("ORCHESTRATOR_URL")
	}
	if cfg.Orchestrator.URL == "" {
		cfg.Orchestrator.URL = "http://localhost:3000"
	}
	if cfg.Orchestrator.APIKey == "" {
		cfg.Orchestrator.APIKey = os.Getenv("ORCHESTRATOR_API_KEY")
	}

	cfg.GraphitiURL = expandEnvVar(cfg.GraphitiURL)
	if cfg.GraphitiURL == "" {
		cfg.GraphitiURL = os.Getenv("GRAPHITI_URL")
	}

	return cfg, nil
}

func expandEnvVar(v string) string {
	if strings.HasPrefix(v, "${") && strings.HasSuffix(v, "}") {
		envVar := v[2 : len(v)-1]
		return os.Getenv(envVar)
	}
	return v
}
