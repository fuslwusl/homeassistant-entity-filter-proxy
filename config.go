package main

import (
	"fmt"
	"os"
	"path"
	"time"

	"gopkg.in/yaml.v3"
)

type Config struct {
	HomeAssistantURL    string        `yaml:"homeassistant_url"`
	AccessToken         string        `yaml:"access_token"`
	ListenAddr          string        `yaml:"listen_addr"`
	DashboardURLPath    string        `yaml:"dashboard_url_path"`
	IncludeAllEntities  bool          `yaml:"include_all_entities"`
	StateUpdateInterval string        `yaml:"state_update_interval"`
	StateUpdateEvery    time.Duration `yaml:"-"`
	ExtraEntities       []string      `yaml:"extra_entities"`
	IncludeEntityGlobs  []string      `yaml:"include_entity_globs"`
	ExcludeEntityGlobs  []string      `yaml:"exclude_entity_globs"`
	Transparent         bool          `yaml:"transparent"`
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
	if !cfg.IncludeAllEntities && cfg.AccessToken == "" {
		return nil, fmt.Errorf("access_token is required when include_all_entities is false")
	}

	if !cfg.IncludeAllEntities {
		if err := validateGlobPatterns("include_entity_globs", cfg.IncludeEntityGlobs); err != nil {
			return nil, err
		}
		if err := validateGlobPatterns("exclude_entity_globs", cfg.ExcludeEntityGlobs); err != nil {
			return nil, err
		}
	}

	if cfg.StateUpdateInterval != "" {
		interval, err := time.ParseDuration(cfg.StateUpdateInterval)
		if err != nil {
			return nil, fmt.Errorf("invalid state_update_interval (%q): %w", cfg.StateUpdateInterval, err)
		}
		if interval <= 0 {
			return nil, fmt.Errorf("state_update_interval must be > 0 when set")
		}
		cfg.StateUpdateEvery = interval
	}

	return cfg, nil
}

func validateGlobPatterns(fieldName string, patterns []string) error {
	for _, pattern := range patterns {
		if _, err := path.Match(pattern, ""); err != nil {
			return fmt.Errorf("invalid glob pattern in %s (%q): %w", fieldName, pattern, err)
		}
	}
	return nil
}
