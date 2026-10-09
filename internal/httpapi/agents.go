package httpapi

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"regexp"
	"strings"

	"github.com/timothydodd/tagalong/internal/model"
)

// agentNameRe keeps agent names short and log/URL friendly.
var agentNameRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,62}$`)

// listAgents handles GET /api/agents: registered agents with live state.
func (s *Server) listAgents(w http.ResponseWriter, r *http.Request) {
	agents, err := s.store.ListAgents()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if s.hub != nil {
		agents = s.hub.Statuses(agents)
	}
	writeJSON(w, http.StatusOK, agents)
}

// createAgent handles POST /api/agents {"name": "..."}. The response carries
// the agent's token — the only time it is ever shown.
func (s *Server) createAgent(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Name string `json:"name"`
	}
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	in.Name = strings.TrimSpace(in.Name)
	if !agentNameRe.MatchString(in.Name) {
		writeErr(w, http.StatusBadRequest, "name must be 1-63 letters, digits, '.', '_' or '-'")
		return
	}

	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	token := hex.EncodeToString(b)

	agent, err := s.store.CreateAgent(in.Name, token)
	if err != nil {
		if strings.Contains(err.Error(), "UNIQUE") {
			writeErr(w, http.StatusConflict, "an agent named "+in.Name+" already exists")
			return
		}
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, struct {
		model.Agent
		Token string `json:"token"`
	}{agent, token})
}

// deleteAgent handles DELETE /api/agents/{id}; the agent's token stops working.
func (s *Server) deleteAgent(w http.ResponseWriter, r *http.Request) {
	id, err := idParam(r)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "invalid id")
		return
	}
	if err := s.store.DeleteAgent(id); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if s.hub != nil {
		s.hub.Forget(id)
	}
	w.WriteHeader(http.StatusNoContent)
}

// getHubStatus handles GET /api/hub: this instance's connection to its hub
// when running as an agent ({"enabled": false} otherwise).
func (s *Server) getHubStatus(w http.ResponseWriter, r *http.Request) {
	if s.hubStatus == nil {
		writeJSON(w, http.StatusOK, model.HubStatus{})
		return
	}
	writeJSON(w, http.StatusOK, s.hubStatus())
}
