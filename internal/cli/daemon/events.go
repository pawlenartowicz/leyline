package daemon

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
)

// Event is one SSE message. Name is the SSE `event:` field; Data is JSON.
type Event struct {
	Name string
	Data any
}

// EventBus fans out events to all connected SSE clients.
type EventBus struct {
	mu      sync.Mutex
	subs    map[chan Event]struct{}
	bufSize int
}

func NewEventBus() *EventBus {
	return &EventBus{
		subs:    map[chan Event]struct{}{},
		bufSize: 32,
	}
}

// Publish sends an event to every subscriber. Slow consumers drop events.
func (b *EventBus) Publish(name string, data any) {
	b.mu.Lock()
	defer b.mu.Unlock()
	for ch := range b.subs {
		select {
		case ch <- Event{Name: name, Data: data}:
		default:
		}
	}
}

func (b *EventBus) subscribe() chan Event {
	ch := make(chan Event, b.bufSize)
	b.mu.Lock()
	b.subs[ch] = struct{}{}
	b.mu.Unlock()
	return ch
}

func (b *EventBus) unsubscribe(ch chan Event) {
	b.mu.Lock()
	delete(b.subs, ch)
	b.mu.Unlock()
	close(ch)
}

// Subscribe returns a channel that receives every event published to the bus
// and a cancel func that detaches the subscriber and closes the channel.
// Slow consumers drop events (buffer of 32). Used by foreground/debug mode
// to mirror events to the terminal.
func (b *EventBus) Subscribe() (<-chan Event, func()) {
	ch := b.subscribe()
	return ch, func() { b.unsubscribe(ch) }
}

// handler is the http.HandlerFunc for /events.
func (b *EventBus) handler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}
	// Flush headers immediately so clients can begin reading.
	w.WriteHeader(http.StatusOK)
	flusher.Flush()

	ch := b.subscribe()
	defer b.unsubscribe(ch)

	ctx := r.Context()
	for {
		select {
		case ev, ok := <-ch:
			if !ok {
				return
			}
			data, err := json.Marshal(ev.Data)
			if err != nil {
				continue
			}
			fmt.Fprintf(w, "event: %s\ndata: %s\n\n", ev.Name, data)
			flusher.Flush()
		case <-ctx.Done():
			return
		}
	}
}
