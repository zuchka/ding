package server

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"time"

	"github.com/zuchka/ding/internal/evaluator"
	"github.com/zuchka/ding/internal/ingester"
	"github.com/zuchka/ding/internal/notifier"
)

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, `{"error":"method not allowed"}`, http.StatusMethodNotAllowed)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Write([]byte(`{"status":"ok"}`))
}

func (s *Server) handleIngest(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, `{"error":"method not allowed"}`, http.StatusMethodNotAllowed)
		return
	}

	// Acquire cfg FIRST so MaxBodyBytes is available for MaxBytesReader.
	s.mu.RLock()
	cfg := s.cfg
	eng := s.engine
	notifiers := s.notifiers
	alertLogger := s.alertLogger
	s.mu.RUnlock()

	r.Body = http.MaxBytesReader(w, r.Body, cfg.Server.MaxBodyBytes)
	body, err := io.ReadAll(r.Body)
	if err != nil {
		if errors.As(err, new(*http.MaxBytesError)) {
			jsonError(w, "request body too large", http.StatusRequestEntityTooLarge)
			return
		}
		jsonError(w, "reading body: "+err.Error(), http.StatusBadRequest)
		return
	}

	format := ingester.DetectFormat(body, r.Header.Get("Content-Type"), cfg.Server.Format)
	var events []ingester.Event
	var parseErr error
	if format == "json" {
		events, parseErr = ingester.ParseJSONLine(body)
	} else {
		events, parseErr = ingester.ParsePrometheusText(body)
	}
	if parseErr != nil {
		jsonError(w, parseErr.Error(), http.StatusBadRequest)
		return
	}

	totalAlerts := s.processEvents(events, notifiers, eng, alertLogger)

	w.Header().Set("Content-Type", "application/json")
	fmt.Fprintf(w, `{"events":%d,"alerts_fired":%d}`, len(events), totalAlerts)
}

func (s *Server) handleRules(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, `{"error":"method not allowed"}`, http.StatusMethodNotAllowed)
		return
	}
	s.mu.RLock()
	eng := s.engine
	s.mu.RUnlock()

	statuses := eng.RulesStatus()
	type ruleResp struct {
		Name        string            `json:"name"`
		Condition   string            `json:"condition"`
		Cooldown    string            `json:"cooldown"`
		CoolingDown map[string]string `json:"cooling_down"`
	}
	resp := make([]ruleResp, len(statuses))
	for i, st := range statuses {
		resp[i] = ruleResp{
			Name:        st.Name,
			Condition:   st.Condition,
			Cooldown:    st.Cooldown,
			CoolingDown: st.CoolingDown,
		}
	}
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(resp); err != nil {
		log.Printf("ding: encoding rules response: %v", err)
	}
}

func (s *Server) handleReload(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, `{"error":"method not allowed"}`, http.StatusMethodNotAllowed)
		return
	}
	if s.configPath == "" {
		jsonError(w, "no config path set", http.StatusInternalServerError)
		return
	}

	// If a reload hook has been registered (e.g. by main.go to handle
	// persistence flush/restore around the swap), use it.
	s.mu.RLock()
	hook := s.reloadHook
	s.mu.RUnlock()
	if hook != nil {
		if err := hook(); err != nil {
			jsonError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"status":"reloaded"}`))
		return
	}

	// Default inline reload (no persistence awareness).
	newEng, newCfg, newNotifiers, newAlertLogger, err := buildFromConfig(s.configPath, s.collector)
	if err != nil {
		jsonError(w, "reload failed: "+err.Error(), http.StatusInternalServerError)
		return
	}
	s.SwapEngine(newEng, newCfg, newNotifiers, newAlertLogger)
	log.Printf("ding: config reloaded from %s", s.configPath)
	w.Header().Set("Content-Type", "application/json")
	w.Write([]byte(`{"status":"reloaded"}`))
}

func (s *Server) handleMetrics(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, `{"error":"method not allowed"}`, http.StatusMethodNotAllowed)
		return
	}
	if s.collector == nil {
		http.Error(w, "metrics not available", http.StatusServiceUnavailable)
		return
	}

	s.mu.RLock()
	notifiers := s.notifiers
	s.mu.RUnlock()

	queueDepth := 0
	for _, n := range notifiers {
		if wn, ok := n.(*notifier.WebhookNotifier); ok {
			queueDepth += wn.QueueDepth()
		}
	}

	w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
	s.collector.WritePrometheus(w, queueDepth)
}

func jsonError(w http.ResponseWriter, msg string, code int) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	data, _ := json.Marshal(map[string]string{"error": msg})
	w.Write(data)
}

func (s *Server) processEvents(events []ingester.Event, notifiers map[string]notifier.Notifier, eng *evaluator.Engine, alertLogger *notifier.AlertLogger) int {
	now := time.Now()
	totalAlerts := 0
	if s.collector != nil {
		s.collector.IncrEvents(int64(len(events)))
	}
	for _, event := range events {
		alerts := eng.Process(event, now)
		totalAlerts += len(alerts)
		for _, alert := range alerts {
			if alertLogger != nil {
				if err := alertLogger.Log(alert); err != nil {
					log.Printf("ding: alert log write error: %v", err)
				}
			}
			if s.collector != nil {
				s.collector.IncrAlerts(alert.Rule)
			}
			for _, name := range alert.Notifiers {
				n, ok := notifiers[name]
				if !ok {
					log.Printf("ding: unknown notifier %q for rule %q", name, alert.Rule)
					continue
				}
				if err := n.Send(alert); err != nil {
					log.Printf("ding: notifier %q error: %v", name, err)
				}
			}
		}
	}
	return totalAlerts
}
