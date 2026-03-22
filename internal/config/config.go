package config

import (
	"fmt"
	"os"
	"time"

	"gopkg.in/yaml.v3"
)

// Duration wraps time.Duration for YAML unmarshaling of strings like "5m".
type Duration struct{ time.Duration }

func (d *Duration) UnmarshalYAML(value *yaml.Node) error {
	dur, err := time.ParseDuration(value.Value)
	if err != nil {
		return fmt.Errorf("invalid duration %q: %w", value.Value, err)
	}
	d.Duration = dur
	return nil
}

type ServerConfig struct {
	Port          int    `yaml:"port"`
	Format        string `yaml:"format"`
	MaxBufferSize int    `yaml:"max_buffer_size"`
}

type NotifierConfig struct {
	Type string `yaml:"type"`
	URL  string `yaml:"url"`
}

type AlertTarget struct {
	Notifier string `yaml:"notifier"`
}

type Rule struct {
	Name        string            `yaml:"name"`
	Match       map[string]string `yaml:"match"`
	Condition   string            `yaml:"condition"`
	Cooldown    time.Duration     `yaml:"-"`
	CooldownRaw Duration          `yaml:"cooldown"`
	Message     string            `yaml:"message"`
	Alert       []AlertTarget     `yaml:"alert"`
}

type Config struct {
	Server    ServerConfig              `yaml:"server"`
	Notifiers map[string]NotifierConfig `yaml:"notifiers"`
	Rules     []Rule                    `yaml:"rules"`
}

// Load reads and parses a ding.yaml file.
func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading config: %w", err)
	}
	var cfg Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parsing config: %w", err)
	}
	// Copy parsed duration values
	for i := range cfg.Rules {
		cfg.Rules[i].Cooldown = cfg.Rules[i].CooldownRaw.Duration
	}
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	return &cfg, nil
}

// Validate sets defaults and checks for semantic errors.
func (cfg *Config) Validate() error {
	if cfg.Server.Port == 0 {
		cfg.Server.Port = 8080
	}
	if cfg.Server.Format == "" {
		cfg.Server.Format = "auto"
	}
	if cfg.Server.MaxBufferSize == 0 {
		cfg.Server.MaxBufferSize = 10000
	}

	validFormats := map[string]bool{"json": true, "prometheus": true, "auto": true}
	if !validFormats[cfg.Server.Format] {
		return fmt.Errorf("invalid server.format %q: must be json, prometheus, or auto", cfg.Server.Format)
	}

	for i, rule := range cfg.Rules {
		if rule.Name == "" {
			return fmt.Errorf("rule[%d]: name is required", i)
		}
		if rule.Condition == "" {
			return fmt.Errorf("rule %q: condition is required", rule.Name)
		}
		for _, target := range rule.Alert {
			if target.Notifier == "stdout" {
				continue
			}
			if _, ok := cfg.Notifiers[target.Notifier]; !ok {
				return fmt.Errorf("rule %q: alert references unknown notifier %q", rule.Name, target.Notifier)
			}
		}
	}
	return nil
}
