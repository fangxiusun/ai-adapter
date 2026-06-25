package proxy

import (
	"encoding/json"
	"fmt"
	"io"
)

type sseWriter struct {
	sink    io.Writer
	flusher func()
}

func newSSEWriter(sink io.Writer, flusher func()) *sseWriter {
	return &sseWriter{sink: sink, flusher: flusher}
}

func (w *sseWriter) writeEvent(event string, data interface{}) {
	if data != nil {
		payload, _ := json.Marshal(data)
		fmt.Fprintf(w.sink, "event: %s\ndata: %s\n\n", event, string(payload))
	} else {
		fmt.Fprintf(w.sink, "event: %s\ndata: {}\n\n", event)
	}
	w.flush()
}

func (w *sseWriter) flush() {
	if w.flusher != nil {
		w.flusher()
	}
}