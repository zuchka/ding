package config_test

import (
	"os"
	"strings"
	"testing"
	"time"

	"github.com/zuchka/ding/internal/config"
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
	if err := cfg.Validate(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.Server.Port != 8080 {
		t.Errorf("expected default port 8080, got %d", cfg.Server.Port)
	}
}

func TestValidate_DefaultTimeouts(t *testing.T) {
	cfg := &config.Config{}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.Server.ReadTimeout.Duration != 5*time.Second {
		t.Errorf("expected ReadTimeout 5s, got %v", cfg.Server.ReadTimeout.Duration)
	}
	if cfg.Server.WriteTimeout.Duration != 10*time.Second {
		t.Errorf("expected WriteTimeout 10s, got %v", cfg.Server.WriteTimeout.Duration)
	}
	if cfg.Server.IdleTimeout.Duration != 60*time.Second {
		t.Errorf("expected IdleTimeout 60s, got %v", cfg.Server.IdleTimeout.Duration)
	}
}

func TestValidate_DefaultMaxBodyBytes(t *testing.T) {
	cfg := &config.Config{}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.Server.MaxBodyBytes != 1<<20 {
		t.Errorf("expected MaxBodyBytes %d, got %d", 1<<20, cfg.Server.MaxBodyBytes)
	}
}

func TestValidate_PersistenceDefaults(t *testing.T) {
	cfg := &config.Config{
		Persistence: config.PersistenceConfig{
			StateFile: "/tmp/ding.state",
		},
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.Persistence.FlushInterval.Duration != 30*time.Second {
		t.Errorf("expected FlushInterval 30s, got %v", cfg.Persistence.FlushInterval.Duration)
	}
}

func TestValidate_PersistenceNoStateFile(t *testing.T) {
	cfg := &config.Config{}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.Persistence.FlushInterval.Duration != 0 {
		t.Errorf("expected FlushInterval 0 when StateFile empty, got %v", cfg.Persistence.FlushInterval.Duration)
	}
}

func TestValidate_WebhookRetryDefaults(t *testing.T) {
	cfg := &config.Config{
		Notifiers: map[string]config.NotifierConfig{
			"my-webhook": {
				Type: "webhook",
				URL:  "https://example.com/hook",
			},
		},
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	nc := cfg.Notifiers["my-webhook"]
	if nc.MaxAttempts != 3 {
		t.Errorf("expected MaxAttempts 3, got %d", nc.MaxAttempts)
	}
	if nc.InitialBackoff.Duration != 1*time.Second {
		t.Errorf("expected InitialBackoff 1s, got %v", nc.InitialBackoff.Duration)
	}
}

func TestValidate_WebhookMissingURL(t *testing.T) {
	cfg := &config.Config{
		Notifiers: map[string]config.NotifierConfig{
			"my-webhook": {
				Type: "webhook",
				URL:  "",
			},
		},
	}
	err := cfg.Validate()
	if err == nil {
		t.Fatal("expected validation error for webhook missing url, got nil")
	}
	if !strings.Contains(err.Error(), "requires a url") {
		t.Errorf("expected error to contain \"requires a url\", got: %v", err)
	}
}

func TestLoad_JQField(t *testing.T) {
	yaml := `
server:
  port: 8080
  jq: '.events[] | {metric: .name, value: .v}'
rules:
  - name: test_rule
    condition: "value > 0"
    alert:
      - notifier: stdout
`
	f, err := os.CreateTemp("", "ding-*.yaml")
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(f.Name())
	f.WriteString(yaml)
	f.Close()

	cfg, err := config.Load(f.Name())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := ".events[] | {metric: .name, value: .v}"
	if cfg.Server.JQ != want {
		t.Errorf("expected JQ %q, got %q", want, cfg.Server.JQ)
	}
}
