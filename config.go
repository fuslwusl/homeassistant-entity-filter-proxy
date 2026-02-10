package main

import (
	"fmt"
	"os"

	"gopkg.in/yaml.v3"
)

type Config struct {
	HomeAssistantURL string   `yaml:"homeassistant_url"`
	AccessToken      string   `yaml:"access_token"`
	ListenAddr       string   `yaml:"listen_addr"`
	DashboardURLPath string   `yaml:"dashboard_url_path"`
	ExtraEntities    []string `yaml:"extra_entities"`
	Transparent      bool     `yaml:"transparent"`
}

func loadConfig(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading config file: %w", err)
	}

	cfg := &Config{
		ListenAddr: ":8124",
	}
	if err := yaml.Unmarshal(data, cfg); err != nil {
		return nil, fmt.Errorf("parsing config file: %w", err)
	}

	if cfg.HomeAssistantURL == "" {
		return nil, fmt.Errorf("homeassistant_url is required")
	}
	if cfg.AccessToken == "" {
		return nil, fmt.Errorf("access_token is required")
	}

	return cfg, nil
}
