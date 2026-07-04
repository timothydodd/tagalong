package httpapi

import (
	"context"
	"net/http"
	"time"
)

// listWorkloads returns Deployments/StatefulSets in the cluster so the UI can
// prefill a new app from an already-deployed workload.
func (s *Server) listWorkloads(w http.ResponseWriter, r *http.Request) {
	if !s.k8s.Configured() {
		writeErr(w, http.StatusServiceUnavailable, "no cluster configured")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	wls, err := s.k8s.ListWorkloads(ctx)
	if err != nil {
		writeErr(w, http.StatusBadGateway, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, wls)
}
