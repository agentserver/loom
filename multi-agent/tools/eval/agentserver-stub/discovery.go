package main

import (
	"encoding/json"
	"net/http"
)

// discoveryCardRequest mirrors the payload slave.internal/tunnel.PublishCard sends.
// See spec §4.1.
type discoveryCardRequest struct {
	DisplayName string          `json:"display_name"`
	Description string          `json:"description"`
	AgentType   string          `json:"agent_type"`
	Card        json.RawMessage `json:"card"`
}

// discoveryAgentEntry is the array element returned by GET /api/agent/discovery/agents.
// Matches agentserver v0.69.9 pkg/agentsdk.AgentCard (spec §4.2).
type discoveryAgentEntry struct {
	AgentID     string          `json:"agent_id"` // = card.SandboxID
	DisplayName string          `json:"display_name"`
	Description string          `json:"description"`
	AgentType   string          `json:"agent_type"`
	Status      string          `json:"status"`
	Card        json.RawMessage `json:"card"`
	Version     int             `json:"version"`
}

func (s *Server) handleDiscoveryCards(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	token, ok := bearerToken(r.Header.Get("Authorization"))
	if !ok {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	s.mu.RLock()
	ident, known := s.byProxy[token]
	s.mu.RUnlock()
	if !known {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	var req discoveryCardRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid json: "+err.Error(), http.StatusBadRequest)
		return
	}
	if req.AgentType == "" {
		req.AgentType = "custom"
	}
	card := agentCard{
		SandboxID:   ident.SandboxID,
		WorkspaceID: ident.WorkspaceID,
		ShortID:     ident.ShortID,
		DisplayName: req.DisplayName,
		Description: req.Description,
		AgentType:   req.AgentType,
		Card:        req.Card,
	}
	s.mu.Lock()
	s.cards[ident.SandboxID] = card
	s.mu.Unlock()
	w.WriteHeader(http.StatusOK)
}

func (s *Server) handleDiscoveryAgents(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	token, ok := bearerToken(r.Header.Get("Authorization"))
	if !ok {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	s.mu.RLock()
	ident, known := s.byProxy[token]
	if !known {
		s.mu.RUnlock()
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	wsID := ident.WorkspaceID
	entries := make([]discoveryAgentEntry, 0, len(s.cards))
	for _, c := range s.cards {
		if c.WorkspaceID != wsID {
			continue
		}
		entries = append(entries, discoveryAgentEntry{
			AgentID:     c.SandboxID,
			DisplayName: c.DisplayName,
			Description: c.Description,
			AgentType:   c.AgentType,
			Status:      "available",
			Card:        c.Card,
			Version:     1,
		})
	}
	s.mu.RUnlock()
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(entries)
}
