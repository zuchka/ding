package server

import (
	"fmt"
	"log"
	"net/http"
	"sync"
	"time"

	"github.com/super-ding/ding/internal/config"
	"github.com/super-ding/ding/internal/evaluator"
	"github.com/super-ding/ding/internal/ingester"
	"github.com/super-ding/ding/internal/notifier"
)

// Server holds the HTTP server state.
type Server struct {
	mu         sync.RWMutex
	engine     *evaluator.Engine
	notifiers  map[string]notifier.Notifier
	cfg        *config.Config
	configPath string
	mux        *http.ServeMux
}

// New creates a Server. configPath is used by /reload.
func New(eng *evaluator.Engine, notifiers map[string]notifier.Notifier, cfg *config.Config, configPath string) *Server {
	s := &Server{
		engine:     eng,
		notifiers:  notifiers,
		cfg:        cfg,
		configPath: configPath,
		mux:        http.NewServeMux(),
	}
	s.mux.HandleFunc("/health", s.handleHealth)
	s.mux.HandleFunc("/ingest", s.handleIngest)
	s.mux.HandleFunc("/rules", s.handleRules)
	s.mux.HandleFunc("/reload", s.handleReload)
	return s
}

// Handler returns the HTTP handler for use in tests or net/http.
func (s *Server) Handler() http.Handler { return s.mux }

// SwapEngine atomically replaces the engine (used by hot-reload).
func (s *Server) SwapEngine(eng *evaluator.Engine, cfg *config.Config, notifiers map[string]notifier.Notifier) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.engine = eng
	s.cfg = cfg
	s.notifiers = notifiers
}

// BuildFromConfig loads a config file and builds an Engine + Notifiers.
// Exported for use in main.go.
func BuildFromConfig(path string) (*evaluator.Engine, *config.Config, map[string]notifier.Notifier, error) {
	return buildFromConfig(path)
}

// buildFromConfig is the internal implementation.
func buildFromConfig(path string) (*evaluator.Engine, *config.Config, map[string]notifier.Notifier, error) {
	cfg, err := config.Load(path)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("loading config: %w", err)
	}

	rules := make([]evaluator.EngineRule, len(cfg.Rules))
	for i, r := range cfg.Rules {
		alerts := make([]string, len(r.Alert))
		for j, a := range r.Alert {
			alerts[j] = a.Notifier
		}
		rules[i] = evaluator.EngineRule{
			Name:      r.Name,
			Match:     r.Match,
			Condition: r.Condition,
			Cooldown:  r.Cooldown,
			Message:   r.Message,
			Alerts:    alerts,
		}
	}
	eng, err := evaluator.NewEngine(rules, cfg.Server.MaxBufferSize)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("building engine: %w", err)
	}

	notifiers := map[string]notifier.Notifier{
		"stdout": notifier.NewStdoutNotifier(nil),
	}
	for name, nc := range cfg.Notifiers {
		if nc.Type == "webhook" {
			notifiers[name] = notifier.NewWebhookNotifier(nc.URL)
		}
	}

	return eng, cfg, notifiers, nil
}

// IngestLine processes a single raw event line from stdin.
func (s *Server) IngestLine(line []byte) {
	s.mu.RLock()
	cfg := s.cfg
	eng := s.engine
	notifiers := s.notifiers
	s.mu.RUnlock()

	format := ingester.DetectFormat(line, "", cfg.Server.Format)
	var events []ingester.Event
	var err error
	if format == "json" {
		events, err = ingester.ParseJSONLine(line)
	} else {
		events, err = ingester.ParsePrometheusText(line)
	}
	if err != nil {
		log.Printf("ding: stdin parse error: %v", err)
		return
	}

	now := time.Now()
	for _, event := range events {
		alerts := eng.Process(event, now)
		for _, alert := range alerts {
			for _, name := range alert.Notifiers {
				n, ok := notifiers[name]
				if !ok {
					continue
				}
				n.Send(alert)
			}
		}
	}
}
