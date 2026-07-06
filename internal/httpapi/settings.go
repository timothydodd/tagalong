package httpapi

import (
	"encoding/json"
	"net/http"
	"net/url"

	"github.com/go-chi/chi/v5"
	"github.com/timothydodd/tagalong/internal/model"
)

// maskedValue is the placeholder returned instead of a stored secret. A PUT that
// echoes this value back leaves the stored secret unchanged.
const maskedValue = "********"

func (s *Server) getSettings(w http.ResponseWriter, r *http.Request) {
	cfToken, _ := s.store.GetSetting(model.KeyCloudflareAPIToken)
	ghSecret, _ := s.store.GetSetting(model.KeyGitHubWebhookSecret)
	writeJSON(w, http.StatusOK, model.Settings{
		CloudflareAPIToken: maskIfSet(cfToken),
		// The GitHub webhook secret is returned in the clear: it's not an external
		// credential, and operators need to read it back to paste into the GitHub
		// webhook config. It is only reachable behind the portal login.
		GitHubWebhookSecret: ghSecret,
	})
}

func (s *Server) putSettings(w http.ResponseWriter, r *http.Request) {
	var in model.Settings
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if in.CloudflareAPIToken != maskedValue {
		if err := s.store.SetSetting(model.KeyCloudflareAPIToken, in.CloudflareAPIToken); err != nil {
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
	}
	if in.GitHubWebhookSecret != maskedValue {
		if err := s.store.SetSetting(model.KeyGitHubWebhookSecret, in.GitHubWebhookSecret); err != nil {
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
	}
	s.getSettings(w, r)
}

func (s *Server) listRegistries(w http.ResponseWriter, r *http.Request) {
	creds, err := s.store.ListRegistryCreds()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	for i := range creds {
		creds[i].Password = maskIfSet(creds[i].Password)
	}
	writeJSON(w, http.StatusOK, creds)
}

func (s *Server) putRegistry(w http.ResponseWriter, r *http.Request) {
	var in model.RegistryCred
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if in.Registry == "" {
		writeErr(w, http.StatusBadRequest, "registry is required")
		return
	}
	// Preserve existing password when the masked placeholder is echoed back.
	if in.Password == maskedValue {
		if existing, ok, _ := s.store.GetRegistryCred(in.Registry); ok {
			in.Password = existing.Password
		}
	}
	if err := s.store.SetRegistryCred(in); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) deleteRegistry(w http.ResponseWriter, r *http.Request) {
	reg, _ := url.PathUnescape(chi.URLParam(r, "registry"))
	if err := s.store.DeleteRegistryCred(reg); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func maskIfSet(v string) string {
	if v == "" {
		return ""
	}
	return maskedValue
}
