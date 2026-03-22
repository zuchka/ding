package server

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"time"

	"github.com/super-ding/ding/internal/ingester"
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

	body, err := io.ReadAll(r.Body)
	if err != nil {
		jsonError(w, "reading body: "+err.Error(), http.StatusBadRequest)
		return
	}

	s.mu.RLock()
	cfg := s.cfg
	eng := s.engine
	notifiers := s.notifiers
	s.mu.RUnlock()

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

	now := time.Now()
	totalAlerts := 0
	for _, event := range events {
		alerts := eng.Process(event, now)
		totalAlerts += len(alerts)
		for _, alert := range alerts {
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
	newEng, newCfg, newNotifiers, err := buildFromConfig(s.configPath)
	if err != nil {
		jsonError(w, "reload failed: "+err.Error(), http.StatusInternalServerError)
		return
	}
	s.SwapEngine(newEng, newCfg, newNotifiers)
	log.Printf("ding: config reloaded from %s", s.configPath)
	w.Header().Set("Content-Type", "application/json")
	w.Write([]byte(`{"status":"reloaded"}`))
}

func jsonError(w http.ResponseWriter, msg string, code int) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	data, _ := json.Marshal(map[string]string{"error": msg})
	w.Write(data)
}
