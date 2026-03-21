// WebSocket broadcaster. Manages a set of subscribers and fans out
// engine events to all of them concurrently.
//
// Architecture:
//   - A single goroutine (run) reads from the engine's event channel.
//   - Each connected client gets a buffered per-client send channel.
//   - The broadcaster NEVER blocks on a slow client — if a client's
//     buffer is full, that client is disconnected (market data is real-time).

package api

import (
	"log"
	"net/http"

	"github.com/agx18/matching-engine/internal/engine"
	"github.com/gorilla/websocket"
)

var upgrader = websocket.Upgrader{
	ReadBufferSize:  1024,
	WriteBufferSize: 4096,
	// TODO: validate origin. For now, allow all
	CheckOrigin: func(r *http.Request) bool { return true },
}

// client represents a single WebSocket subscriber.
type client struct {
	conn   *websocket.Conn
	sendCh chan engine.TradeEvent
}

// Broadcaster fans out TradeEvents from the engine to all connected clients.
type Broadcaster struct {
	eventCh    <-chan engine.TradeEvent
	clients    map[*client]struct{}
	register   chan *client
	unregister chan *client
}

// New creates a Broadcaster that reads from the engine's event channel.
func New(eventCh <-chan engine.TradeEvent) *Broadcaster {
	return &Broadcaster{
		eventCh:    eventCh,
		clients:    make(map[*client]struct{}),
		register:   make(chan *client, 16),
		unregister: make(chan *client, 16),
	}
}

// Run starts the broadcaster's main loop. Call in a dedicated goroutine.
// All mutations to the clients map happen here and nowhere else
func (b *Broadcaster) Run() {
	for {
		select {
		case c := <-b.register:
			b.clients[c] = struct{}{}

		case c := <-b.unregister:
			if _, ok := b.clients[c]; ok {
				delete(b.clients, c)
				close(c.sendCh)
			}

		case event, ok := <-b.eventCh:
			if !ok {
				for c := range b.clients {
					close(c.sendCh)
				}
				return
			}
			b.broadcast(event)
		}
	}
}

// broadcast fans out an event to all connected clients.
// Called only from Run() — shares its goroutine, so clients map access is safe.
// Slow clients (full sendCh buffer) are collected and removed after the loop
// rather than during it, avoiding modification of the map while ranging over it.
func (b *Broadcaster) broadcast(event engine.TradeEvent) {
	var slow []*client
	for c := range b.clients {
		select {
		case c.sendCh <- event:
		default:
			// Client's buffer is full — mark for disconnection.
			// Collected here, removed below to avoid mutating the map mid-range.
			slow = append(slow, c)
		}
	}
	for _, c := range slow {
		log.Println("[broadcaster] slow client, disconnecting")
		delete(b.clients, c)
		close(c.sendCh)
	}
}

// ServeWS upgrades an HTTP connection to WebSocket and registers the client.
// This is the HTTP handler for GET /ws.
func (b *Broadcaster) ServeWS(w http.ResponseWriter, r *http.Request) {
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Printf("[broadcaster] upgrade error: %v", err)
		return
	}

	c := &client{
		conn:   conn,
		sendCh: make(chan engine.TradeEvent, 64),
	}

	b.register <- c

	// writePump: drains sendCh → WebSocket in a dedicated goroutine per client.
	go func() {
		defer func() {
			b.unregister <- c
			_ = conn.Close()
		}()

		for event := range c.sendCh {
			if err := conn.WriteJSON(event); err != nil {
				log.Printf("[broadcaster] write error: %v", err)
				return
			}
		}
	}()

	// readPump: we don't expect messages from clients (read-only feed),
	// but we must read to detect disconnections and handle pings.
	go func() {
		defer func() { b.unregister <- c }()
		for {
			if _, _, err := conn.ReadMessage(); err != nil {
				return // client disconnected
			}
		}
	}()
}
