package websocket

import (
	"net/http"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

var upgrader = websocket.Upgrader{
	CheckOrigin: func(r *http.Request) bool { return true },
}

// Hub manages WebSocket connections and broadcasts messages.
type Hub struct {
	mu          sync.RWMutex
	clients     map[*websocket.Conn]bool
	broadcastCh chan Message
	register    chan *websocket.Conn
	unregister  chan *websocket.Conn
	done        chan struct{}
}

// Message represents a WebSocket message.
type Message struct {
	Type string      `json:"type"`
	Data interface{} `json:"data"`
}

// NewHub creates a new WebSocket Hub.
func NewHub() *Hub {
	return &Hub{
		clients:     make(map[*websocket.Conn]bool),
		broadcastCh: make(chan Message, 256),
		register:    make(chan *websocket.Conn),
		unregister:  make(chan *websocket.Conn),
		done:        make(chan struct{}),
	}
}

// Run starts the Hub's main loop.
func (h *Hub) Run() {
	for {
		select {
		case <-h.done:
			h.mu.Lock()
			for conn := range h.clients {
				conn.Close()
			}
			h.clients = make(map[*websocket.Conn]bool)
			h.mu.Unlock()
			return
		case conn := <-h.register:
			h.mu.Lock()
			h.clients[conn] = true
			h.mu.Unlock()
		case conn := <-h.unregister:
			h.mu.Lock()
			delete(h.clients, conn)
			conn.Close()
			h.mu.Unlock()
		case msg := <-h.broadcastCh:
			h.mu.RLock()
			for conn := range h.clients {
				conn.WriteJSON(msg)
			}
			h.mu.RUnlock()
		}
	}
}

// Broadcast sends a message to all connected clients.
func (h *Hub) Broadcast(msgType string, data interface{}) {
	select {
	case h.broadcastCh <- Message{Type: msgType, Data: data}:
	default:
		// Channel full, drop message
	}
}

// ServeHTTP handles WebSocket upgrade requests.
func (h *Hub) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	h.register <- conn

	// Read loop (handle client disconnect)
	go func() {
		defer func() { h.unregister <- conn }()
		for {
			_, _, err := conn.ReadMessage()
			if err != nil {
				break
			}
		}
	}()
}

// Stop gracefully shuts down the Hub, closing all client connections.
func (h *Hub) Stop() {
	close(h.done)
}

// StartHeartbeat sends periodic heartbeat messages.
func (h *Hub) StartHeartbeat(getActive func() int) {
	go func() {
		ticker := time.NewTicker(30 * time.Second)
		defer ticker.Stop()
		for range ticker.C {
			h.Broadcast("heartbeat", map[string]interface{}{
				"active_requests": getActive(),
			})
		}
	}()
}
