// REST endpoints. These handlers run in concurrent HTTP goroutines.
// They communicate with the engine ONLY through the command channel —
// they never touch the OrderBook directly.
//
// POST /orders        → place a new order
// DELETE /orders/{id} → cancel a resting order
// GET  /book          → current order book snapshot
// GET    /health      → liveness check

package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/agx18/matching-engine/internal/engine"
	"github.com/agx18/matching-engine/internal/orderbook"
)

const (
	// maxRequestBodyBytes caps incoming JSON payloads at 1MB.
	// Prevents a malicious client from sending a multi-GB body and forcing
	// the server to allocate unbounded RAM — a trivial OOM attack vector.
	maxRequestBodyBytes = 1 << 20 // 1MB

	// engineTimeout is how long a handler will wait for the engine to respond
	// before returning 503. Protects against engine stalls exhausting all
	// HTTP goroutines and file descriptors.
	engineTimeout = 2 * time.Second
)

// Handler holds dependencies for all REST handlers.
type Handler struct {
	engineCh chan<- orderbook.Command
	eng      *engine.Engine       // needed for book snapshot endpoint
	book     *orderbook.OrderBook // read-only snapshot access
}

// NewHandler constructs the REST handler.
func NewHandler(eng *engine.Engine, cmdCh chan<- orderbook.Command) *Handler {
	return &Handler{engineCh: cmdCh, eng: eng}
}

// RegisterRoutes wires up all routes on the given router.
func (h *Handler) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("POST /orders", h.PlaceOrder)
	mux.HandleFunc("DELETE /orders/{id}", h.CancelOrder)
	mux.HandleFunc("GET /health", h.Health)
}

// ---- Request / Response types ----

type PlaceOrderRequest struct {
	Side     string `json:"side"`
	Type     string `json:"type"`
	Price    int64  `json:"price"`
	Quantity int64  `json:"quantity"`
	ClientID uint64 `json:"client_id"` // uint64 — matches Order.ClientID
}

type PlaceOrderResponse struct {
	OrderID uint64             `json:"order_id"` // uint64 Snowflake, converted to string at boundary
	Trades  []*orderbook.Trade `json:"trades"`
	Message string             `json:"message,omitempty"`
}

type ErrorResponse struct {
	Error string `json:"error"`
}

// ---- Handlers ----

// PlaceOrder handles POST /orders
func (h *Handler) PlaceOrder(w http.ResponseWriter, r *http.Request) {
	// Cap the request body to prevent OOM from malicious oversized payloads.
	r.Body = http.MaxBytesReader(w, r.Body, maxRequestBodyBytes)

	var req PlaceOrderRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		jsonError(w, "invalid request body: "+err.Error(), http.StatusBadRequest)
		return
	}

	order := &orderbook.Order{
		Quantity:  req.Quantity,
		ClientID:  req.ClientID,          // uint64 — direct assignment, no parse needed
		Timestamp: time.Now().UnixNano(), // int64 Unix nanos
	}

	// Parse side.
	switch req.Side {
	case "BUY":
		order.Side = orderbook.Buy
	case "SELL":
		order.Side = orderbook.Sell
	default:
		jsonError(w, "side must be BUY or SELL", http.StatusBadRequest)
		return
	}

	// Parse type.
	switch req.Type {
	case "LIMIT":
		order.Type = orderbook.LimitOrder
		order.Price = req.Price
	case "MARKET":
		order.Type = orderbook.MarketOrder
	default:
		jsonError(w, "type must be LIMIT or MARKET", http.StatusBadRequest)
		return
	}

	result := h.sendCommand(r, orderbook.Command{
		Type:  orderbook.CmdPlaceOrder,
		Order: order,
	})
	if result.Err != nil {
		jsonError(w, result.Err.Error(), http.StatusUnprocessableEntity)
		return
	}

	trades := result.Trades
	if trades == nil {
		trades = []*orderbook.Trade{} // return [] not null
	}

	jsonOK(w, PlaceOrderResponse{
		OrderID: result.OrderID,
		Trades:  trades,
		Message: tradeMessage(len(trades)),
	})
}

// CancelOrder handles DELETE /orders/{id}
func (h *Handler) CancelOrder(w http.ResponseWriter, r *http.Request) {
	rawID := r.PathValue("id")
	if rawID == "" {
		jsonError(w, "order id required", http.StatusBadRequest)
		return
	}

	// URL params are always strings — parse to uint64 at the API boundary.
	// Inside the engine, IDs are always uint64.
	orderID, err := strconv.ParseUint(rawID, 10, 64)
	if err != nil {
		jsonError(w, "order id must be a valid uint64", http.StatusBadRequest)
		return
	}

	result := h.sendCommand(r, orderbook.Command{
		Type:    orderbook.CmdCancelOrder,
		OrderID: orderID,
	})
	if result.Err != nil {
		jsonError(w, result.Err.Error(), http.StatusNotFound)
		return
	}

	jsonOK(w, map[string]uint64{"cancelled": result.OrderID})
}

// Health handles GET /health
func (h *Handler) Health(w http.ResponseWriter, r *http.Request) {
	jsonOK(w, map[string]string{"status": "ok", "ts": strconv.FormatInt(time.Now().UnixNano(), 10)})
}

// GetBook handles GET /book?depth=N
// Returns a snapshot of the top N price levels on each side.
// depth defaults to 10 if not specified or invalid.
// The snapshot is taken inside the engine goroutine — safe, no data race.
func (h *Handler) GetBook(w http.ResponseWriter, r *http.Request) {
	depth := 10
	if d := r.URL.Query().Get("depth"); d != "" {
		if parsed, err := strconv.Atoi(d); err == nil && parsed > 0 {
			depth = parsed
		}
	}

	result := h.sendCommand(r, orderbook.Command{
		Type:  orderbook.CmdGetBook,
		Depth: depth,
	})
	if result.Err != nil {
		jsonError(w, result.Err.Error(), http.StatusInternalServerError)
		return
	}

	jsonOK(w, result.BookSnapshot)
}

// ---- Internal helpers ----

// sendCommand dispatches a command to the engine and waits for the result.
// It respects two cancellation signals:
//
//  1. r.Context().Done() — the client dropped the connection. No point waiting
//     for a result nobody will receive.
//
//  2. engineTimeout — the engine is stalled or backed up. Return 503 rather
//     than holding the HTTP goroutine (and its file descriptor) indefinitely.
//
// Note: if the context fires after the command was already sent, the engine
// will still process it — we just don't wait for the result. This is
// acceptable; the order may rest in the book even if the client timed out.
func (h *Handler) sendCommand(r *http.Request, cmd orderbook.Command) orderbook.CommandResult {
	cmd.ResultCh = make(chan orderbook.CommandResult, 1)

	// Send the command — may block briefly if commandCh buffer is full (backpressure).
	select {
	case h.engineCh <- cmd:
		// command accepted — now wait for the result
	case <-r.Context().Done():
		return orderbook.CommandResult{Err: fmt.Errorf("request cancelled before engine accepted command")}
	case <-time.After(engineTimeout):
		return orderbook.CommandResult{Err: fmt.Errorf("engine timeout: command queue full")}
	}

	// Wait for the engine to process and reply.
	select {
	case result := <-cmd.ResultCh:
		return result
	case <-r.Context().Done():
		return orderbook.CommandResult{Err: fmt.Errorf("request cancelled or timed out")}
	case <-time.After(engineTimeout):
		return orderbook.CommandResult{Err: fmt.Errorf("engine timeout: no response")}
	}
}

func jsonOK(w http.ResponseWriter, payload any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(payload)
}

func jsonError(w http.ResponseWriter, msg string, code int) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(ErrorResponse{Error: msg})
}

func tradeMessage(n int) string {
	if n == 0 {
		return "order resting in book"
	}
	if n == 1 {
		return "1 trade executed"
	}
	return strconv.Itoa(n) + " trades executed"
}
