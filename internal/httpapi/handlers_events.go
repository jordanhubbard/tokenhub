package httpapi

import (
	"fmt"
	"net/http"

	"github.com/jordanhubbard/tokenhub/internal/events"
)

// SSEHandler streams routing events to the client using Server-Sent Events.
func SSEHandler(bus *events.Bus) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		flusher, ok := w.(http.Flusher)
		if !ok {
			jsonError(w, "streaming unsupported", http.StatusInternalServerError)
			return
		}

		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("Connection", "keep-alive")

		sub := bus.Subscribe(64)
		defer bus.Unsubscribe(sub)

		// Send initial connection event.
		_, _ = fmt.Fprintf(w, "event: connected\ndata: {\"status\":\"ok\"}\n\n")
		flusher.Flush()

		for {
			select {
			case <-r.Context().Done():
				return
			case e := <-sub.C:
				_, _ = fmt.Fprintf(w, "event: %s\ndata: %s\n\n", e.Type, e.JSON())
				flusher.Flush()
			}
		}
	}
}
