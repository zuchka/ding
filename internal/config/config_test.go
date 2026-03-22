package config_test

import (
	"os"
	"testing"
	"time"

	"github.com/super-ding/ding/internal/config"
)

const validYAML = `
server:
  port: 8080
  format: json
  max_buffer_size: 5000

notifiers:
  alert-slack:
    type: webhook
    url: https://hooks.slack.com/test

rules:
  - name: cpu_spike
    match:
      metric: cpu_usage
    condition: "value > 95"
    cooldown: 1m
    message: "CPU spike: {{ .value }}"
    alert:
      - notifier: alert-slack
      - notifier: stdout
`

func TestLoad_Valid(t *testing.T) {
	f, err := os.CreateTemp("", "ding-*.yaml")
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(f.Name())
	f.WriteString(validYAML)
	f.Close()

	cfg, err := config.Load(f.Name())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if cfg.Server.Port != 8080 {
		t.Errorf("expected port 8080, got %d", cfg.Server.Port)
	}
	if cfg.Server.Format != "json" {
		t.Errorf("expected format json, got %s", cfg.Server.Format)
	}
	if cfg.Server.MaxBufferSize != 5000 {
		t.Errorf("expected max_buffer_size 5000, got %d", cfg.Server.MaxBufferSize)
	}
	if len(cfg.Rules) != 1 {
		t.Fatalf("expected 1 rule, got %d", len(cfg.Rules))
	}
	r := cfg.Rules[0]
	if r.Name != "cpu_spike" {
		t.Errorf("expected rule name cpu_spike, got %s", r.Name)
	}
	if r.Cooldown != time.Minute {
		t.Errorf("expected cooldown 1m, got %v", r.Cooldown)
	}
	if len(r.Alert) != 2 {
		t.Fatalf("expected 2 alert targets, got %d", len(r.Alert))
	}
	if r.Alert[0].Notifier != "alert-slack" {
		t.Errorf("expected alert-slack, got %s", r.Alert[0].Notifier)
	}
}

func TestLoad_FileNotFound(t *testing.T) {
	_, err := config.Load("/nonexistent/ding.yaml")
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}

func TestValidate_MissingNotifier(t *testing.T) {
	cfg := &config.Config{
		Rules: []config.Rule{
			{
				Name:      "test",
				Condition: "value > 10",
				Alert:     []config.AlertTarget{{Notifier: "nonexistent"}},
			},
		},
	}
	if err := cfg.Validate(); err == nil {
		t.Fatal("expected validation error for missing notifier reference")
	}
}

func TestValidate_DefaultPort(t *testing.T) {
	cfg := &config.Config{}
	_ = cfg.Validate()
	if cfg.Server.Port != 8080 {
		t.Errorf("expected default port 8080, got %d", cfg.Server.Port)
	}
}
