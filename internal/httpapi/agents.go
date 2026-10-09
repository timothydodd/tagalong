package httpapi

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/url"
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

// getHubStatus handles GET /api/hub: this instance's agent-mode connection to
// a hub ({"enabled": false} when not configured). The token is masked.
func (s *Server) getHubStatus(w http.ResponseWriter, r *http.Request) {
	if s.agent == nil {
		writeJSON(w, http.StatusOK, model.HubStatus{})
		return
	}
	st := s.agent.Status()
	if !st.Locked {
		// UI-managed: report the saved URL even while (re)connecting.
		st.URL, _ = s.store.GetSetting(model.KeyHubURL)
		tok, _ := s.store.GetSetting(model.KeyHubAgentToken)
		st.Token = maskIfSet(tok)
	} else {
		st.Token = maskedValue
	}
	writeJSON(w, http.StatusOK, st)
}

// putHubConfig handles PUT /api/hub {"url": "...", "token": "..."}: saves the
// hub connection and reconnects immediately. An empty url disconnects. A token
// of "********" keeps the stored one. Refused (409) when env vars manage it.
func (s *Server) putHubConfig(w http.ResponseWriter, r *http.Request) {
	if s.agent == nil {
		writeErr(w, http.StatusNotFound, "agent mode unavailable")
		return
	}
	if s.agent.Locked() {
		writeErr(w, http.StatusConflict, "hub connection is set by TAGALONG_HUB_URL / TAGALONG_AGENT_TOKEN; change it there")
		return
	}
	var in struct {
		URL   string `json:"url"`
		Token string `json:"token"`
	}
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	hubURL := strings.TrimRight(strings.TrimSpace(in.URL), "/")
	token := strings.TrimSpace(in.Token)
	if token == maskedValue {
		token, _ = s.store.GetSetting(model.KeyHubAgentToken)
	}
	if hubURL != "" {
		u, err := url.Parse(hubURL)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			writeErr(w, http.StatusBadRequest, "hub URL must be an http(s) address, e.g. https://tagalong.example.com")
			return
		}
		if token == "" {
			writeErr(w, http.StatusBadRequest, "agent token is required (create one in the hub's Settings → Agents)")
			return
		}
	} else {
		token = "" // disconnecting forgets the token too
	}

	if err := s.store.SetSetting(model.KeyHubURL, hubURL); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if err := s.store.SetSetting(model.KeyHubAgentToken, token); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.agent.Apply(hubURL, token)
	s.getHubStatus(w, r)
}
