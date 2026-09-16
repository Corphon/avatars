package stage

import (
	"encoding/json"
	"fmt"
	"net/http"
)

func (s *Server) handleSSE(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	stream, unsubscribe := s.store.Subscribe()
	defer unsubscribe()

	for {
		select {
		case <-r.Context().Done():
			return
		case event := <-stream:
			payload, err := json.Marshal(event)
			if err != nil {
				continue
			}
			_, _ = fmt.Fprintf(w, "data: %s\n\n", payload)
			flusher.Flush()
		}
	}
}
