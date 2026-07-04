package httpapi

import (
	"encoding/json"
	"net/http"
	"strconv"
)

func (s *Server) listEvents(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	appID, _ := strconv.ParseInt(q.Get("app_id"), 10, 64)
	beforeID, _ := strconv.ParseInt(q.Get("before_id"), 10, 64)
	limit, _ := strconv.Atoi(q.Get("limit"))

	events, err := s.store.ListEvents(appID, beforeID, limit)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, events)
}

// streamEvents pushes deploy events to the client over Server-Sent Events.
func (s *Server) streamEvents(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeErr(w, http.StatusInternalServerError, "streaming unsupported")
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no") // disable proxy buffering (nginx)

	ch, unsubscribe := s.bus.Subscribe()
	defer unsubscribe()

	// Initial comment so the client knows the stream is open.
	w.Write([]byte(": connected\n\n"))
	flusher.Flush()

	for {
		select {
		case <-r.Context().Done():
			return
		case ev, ok := <-ch:
			if !ok {
				return
			}
			data, err := json.Marshal(ev)
			if err != nil {
				continue
			}
			w.Write([]byte("event: deploy_event\ndata: "))
			w.Write(data)
			w.Write([]byte("\n\n"))
			flusher.Flush()
		}
	}
}
