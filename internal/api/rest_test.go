// internal/api/rest_test.go
//
// Tests for the REST API layer.
//
// Strategy: spin up a real engine, wire a real Handler to it, and send
// actual HTTP requests through httptest.NewRecorder(). This tests the full
// HTTP → channel → engine → response path without a network socket.

package api_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/agx18/matching-engine/internal/api"
	"github.com/agx18/matching-engine/internal/engine"
	"github.com/agx18/matching-engine/internal/orderbook"
)

// Local response types — mirrors the api package types.
// Defined here because rest_test.go is package api_test (external)
// and cannot access unexported types from package api.
type placeOrderResponse struct {
	OrderID uint64             `json:"order_id"`
	Trades  []*orderbook.Trade `json:"trades"`
	Message string             `json:"message"`
}

type errorResponse struct {
	Error string `json:"error"`
}

// ---------------------------------------------------------------------------
// Test helpers
// ---------------------------------------------------------------------------

// testServer wires a real engine to a real Handler and returns both.
// The engine is started in a goroutine and cleaned up via t.Cleanup.
func testServer(t *testing.T) (*api.Handler, *http.ServeMux) {
	t.Helper()
	eng := engine.New("BTC/USD", 256, 512)
	go eng.Run()
	t.Cleanup(func() { eng.Close() })

	h := api.NewHandler(eng.CommandCh())
	mux := http.NewServeMux()
	h.RegisterRoutes(mux)
	return h, mux
}

// postOrder sends a POST /orders request and returns the response.
func postOrder(t *testing.T, mux *http.ServeMux, body any) *httptest.ResponseRecorder {
	t.Helper()
	b, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal error: %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, "/orders", bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	return w
}

// deleteOrder sends a DELETE /orders/{id} request and returns the response.
func deleteOrder(t *testing.T, mux *http.ServeMux, id uint64) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodDelete, fmt.Sprintf("/orders/%d", id), nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	return w
}

// getBook sends a GET /book request and returns the response.
func getBook(t *testing.T, mux *http.ServeMux, depth int) *httptest.ResponseRecorder {
	t.Helper()
	url := "/book"
	if depth > 0 {
		url = fmt.Sprintf("/book?depth=%d", depth)
	}
	req := httptest.NewRequest(http.MethodGet, url, nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	return w
}

// decodeBody decodes the response body into the target type.
func decodeBody[T any](t *testing.T, w *httptest.ResponseRecorder) T {
	t.Helper()
	var v T
	if err := json.NewDecoder(w.Body).Decode(&v); err != nil {
		t.Fatalf("decode error: %v", err)
	}
	return v
}

// ---------------------------------------------------------------------------
// Health
// ---------------------------------------------------------------------------

func TestHealth_Returns200(t *testing.T) {
	_, mux := testServer(t)
	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("status: got %d, want 200", w.Code)
	}

	body := decodeBody[map[string]string](t, w)
	if body["status"] != "ok" {
		t.Errorf("status field: got %q, want \"ok\"", body["status"])
	}
	if body["ts"] == "" {
		t.Error("ts field should not be empty")
	}
}

// ---------------------------------------------------------------------------
// POST /orders — input validation
// ---------------------------------------------------------------------------

func TestPlaceOrder_InvalidJSON_Returns400(t *testing.T) {
	_, mux := testServer(t)
	req := httptest.NewRequest(http.MethodPost, "/orders", bytes.NewBufferString("not json"))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("status: got %d, want 400", w.Code)
	}
}

func TestPlaceOrder_InvalidSide_Returns400(t *testing.T) {
	_, mux := testServer(t)
	w := postOrder(t, mux, map[string]any{
		"side": "INVALID", "type": "LIMIT", "price": 100, "quantity": 10,
	})
	if w.Code != http.StatusBadRequest {
		t.Errorf("status: got %d, want 400", w.Code)
	}
	body := decodeBody[errorResponse](t, w)
	if body.Error == "" {
		t.Error("expected error message in response body")
	}
}

func TestPlaceOrder_InvalidType_Returns400(t *testing.T) {
	_, mux := testServer(t)
	w := postOrder(t, mux, map[string]any{
		"side": "BUY", "type": "FOK", "price": 100, "quantity": 10,
	})
	if w.Code != http.StatusBadRequest {
		t.Errorf("status: got %d, want 400", w.Code)
	}
}

func TestPlaceOrder_ZeroQuantity_Returns422(t *testing.T) {
	_, mux := testServer(t)
	w := postOrder(t, mux, map[string]any{
		"side": "BUY", "type": "LIMIT", "price": 100, "quantity": 0,
	})
	if w.Code != http.StatusUnprocessableEntity {
		t.Errorf("status: got %d, want 422", w.Code)
	}
}

func TestPlaceOrder_ZeroLimitPrice_Returns422(t *testing.T) {
	_, mux := testServer(t)
	w := postOrder(t, mux, map[string]any{
		"side": "BUY", "type": "LIMIT", "price": 0, "quantity": 10,
	})
	if w.Code != http.StatusUnprocessableEntity {
		t.Errorf("status: got %d, want 422", w.Code)
	}
}

func TestPlaceOrder_OversizedBody_Returns400(t *testing.T) {
	_, mux := testServer(t)
	// Send a body larger than 1MB (maxRequestBodyBytes).
	huge := make([]byte, (1<<20)+1)
	req := httptest.NewRequest(http.MethodPost, "/orders", bytes.NewReader(huge))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("status: got %d, want 400", w.Code)
	}
}

// ---------------------------------------------------------------------------
// POST /orders — successful placement
// ---------------------------------------------------------------------------

func TestPlaceOrder_LimitOrderResting_Returns200WithEmptyTrades(t *testing.T) {
	_, mux := testServer(t)
	w := postOrder(t, mux, map[string]any{
		"side": "BUY", "type": "LIMIT", "price": 100, "quantity": 10,
	})

	if w.Code != http.StatusOK {
		t.Fatalf("status: got %d, want 200", w.Code)
	}

	body := decodeBody[placeOrderResponse](t, w)
	if body.OrderID == 0 {
		t.Error("expected non-zero order ID")
	}
	if len(body.Trades) != 0 {
		t.Errorf("expected no trades, got %d", len(body.Trades))
	}
	if body.Message != "order resting in book" {
		t.Errorf("message: got %q, want \"order resting in book\"", body.Message)
	}
}

func TestPlaceOrder_MatchingOrders_ReturnsTrades(t *testing.T) {
	_, mux := testServer(t)

	// Place resting sell.
	postOrder(t, mux, map[string]any{
		"side": "SELL", "type": "LIMIT", "price": 100, "quantity": 10,
	})

	// Place matching buy.
	w := postOrder(t, mux, map[string]any{
		"side": "BUY", "type": "LIMIT", "price": 100, "quantity": 10,
	})

	if w.Code != http.StatusOK {
		t.Fatalf("status: got %d, want 200", w.Code)
	}

	body := decodeBody[placeOrderResponse](t, w)
	if len(body.Trades) != 1 {
		t.Fatalf("expected 1 trade, got %d", len(body.Trades))
	}
	if body.Trades[0].Price != 100 {
		t.Errorf("trade price: got %d, want 100", body.Trades[0].Price)
	}
	if body.Trades[0].Quantity != 10 {
		t.Errorf("trade quantity: got %d, want 10", body.Trades[0].Quantity)
	}
	if body.Message != "1 trade executed" {
		t.Errorf("message: got %q, want \"1 trade executed\"", body.Message)
	}
}

func TestPlaceOrder_MultipleTradesMessage(t *testing.T) {
	_, mux := testServer(t)

	// Place two resting sells.
	postOrder(t, mux, map[string]any{
		"side": "SELL", "type": "LIMIT", "price": 100, "quantity": 5,
	})
	postOrder(t, mux, map[string]any{
		"side": "SELL", "type": "LIMIT", "price": 101, "quantity": 5,
	})

	// Buy enough to match both.
	w := postOrder(t, mux, map[string]any{
		"side": "BUY", "type": "LIMIT", "price": 101, "quantity": 10,
	})

	body := decodeBody[placeOrderResponse](t, w)
	if len(body.Trades) != 2 {
		t.Fatalf("expected 2 trades, got %d", len(body.Trades))
	}
	if body.Message != "2 trades executed" {
		t.Errorf("message: got %q, want \"2 trades executed\"", body.Message)
	}
}

func TestPlaceOrder_TradesFieldIsArrayNotNull(t *testing.T) {
	_, mux := testServer(t)

	// Place a resting order — no trades.
	w := postOrder(t, mux, map[string]any{
		"side": "BUY", "type": "LIMIT", "price": 100, "quantity": 10,
	})

	// Parse raw JSON to check the trades field is [] not null.
	var raw map[string]json.RawMessage
	if err := json.NewDecoder(w.Body).Decode(&raw); err != nil {
		t.Fatalf("decode error: %v", err)
	}
	if string(raw["trades"]) == "null" {
		t.Error("trades field should be [] not null")
	}
}

func TestPlaceOrder_MarketOrder_NoPrice(t *testing.T) {
	_, mux := testServer(t)

	// Place resting sell first.
	postOrder(t, mux, map[string]any{
		"side": "SELL", "type": "LIMIT", "price": 100, "quantity": 10,
	})

	// Market buy — no price field needed.
	w := postOrder(t, mux, map[string]any{
		"side": "BUY", "type": "MARKET", "quantity": 5,
	})

	if w.Code != http.StatusOK {
		t.Fatalf("status: got %d, want 200", w.Code)
	}
	body := decodeBody[placeOrderResponse](t, w)
	if len(body.Trades) != 1 {
		t.Fatalf("expected 1 trade, got %d", len(body.Trades))
	}
}

// ---------------------------------------------------------------------------
// DELETE /orders/{id}
// ---------------------------------------------------------------------------

func TestCancelOrder_ValidID_Returns200(t *testing.T) {
	_, mux := testServer(t)

	// Place a resting order.
	w := postOrder(t, mux, map[string]any{
		"side": "BUY", "type": "LIMIT", "price": 100, "quantity": 10,
	})
	body := decodeBody[placeOrderResponse](t, w)

	// Cancel it.
	wc := deleteOrder(t, mux, body.OrderID)
	if wc.Code != http.StatusOK {
		t.Fatalf("cancel status: got %d, want 200", wc.Code)
	}

	var result map[string]uint64
	json.NewDecoder(wc.Body).Decode(&result)
	if result["cancelled"] != body.OrderID {
		t.Errorf("cancelled ID: got %d, want %d", result["cancelled"], body.OrderID)
	}
}

func TestCancelOrder_UnknownID_Returns404(t *testing.T) {
	_, mux := testServer(t)
	w := deleteOrder(t, mux, 99999)
	if w.Code != http.StatusNotFound {
		t.Errorf("status: got %d, want 404", w.Code)
	}
}

func TestCancelOrder_InvalidID_Returns400(t *testing.T) {
	_, mux := testServer(t)
	req := httptest.NewRequest(http.MethodDelete, "/orders/not-a-number", nil)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Errorf("status: got %d, want 400", w.Code)
	}
}

func TestCancelOrder_AlreadyFilled_Returns404(t *testing.T) {
	_, mux := testServer(t)

	// Place and fully match an order.
	postOrder(t, mux, map[string]any{
		"side": "SELL", "type": "LIMIT", "price": 100, "quantity": 10,
	})
	w := postOrder(t, mux, map[string]any{
		"side": "BUY", "type": "LIMIT", "price": 100, "quantity": 10,
	})
	body := decodeBody[placeOrderResponse](t, w)

	// Try to cancel the filled sell order.
	wc := deleteOrder(t, mux, body.Trades[0].SellOrderID)
	if wc.Code != http.StatusNotFound {
		t.Errorf("status: got %d, want 404", wc.Code)
	}
}

// ---------------------------------------------------------------------------
// GET /book
// ---------------------------------------------------------------------------

func TestGetBook_EmptyBook_ReturnsEmptyLevels(t *testing.T) {
	_, mux := testServer(t)
	w := getBook(t, mux, 5)

	if w.Code != http.StatusOK {
		t.Fatalf("status: got %d, want 200", w.Code)
	}

	body := decodeBody[orderbook.BookSnapshot](t, w)
	if body.Symbol != "BTC/USD" {
		t.Errorf("symbol: got %q, want \"BTC/USD\"", body.Symbol)
	}
	if len(body.Bids) != 0 {
		t.Errorf("expected empty bids, got %d levels", len(body.Bids))
	}
	if len(body.Asks) != 0 {
		t.Errorf("expected empty asks, got %d levels", len(body.Asks))
	}
}

func TestGetBook_ReturnsCorrectLevels(t *testing.T) {
	_, mux := testServer(t)

	// Place orders on both sides.
	postOrder(t, mux, map[string]any{
		"side": "BUY", "type": "LIMIT", "price": 90, "quantity": 5,
	})
	postOrder(t, mux, map[string]any{
		"side": "SELL", "type": "LIMIT", "price": 110, "quantity": 3,
	})

	w := getBook(t, mux, 5)
	if w.Code != http.StatusOK {
		t.Fatalf("status: got %d, want 200", w.Code)
	}

	body := decodeBody[orderbook.BookSnapshot](t, w)
	if len(body.Bids) != 1 {
		t.Fatalf("expected 1 bid level, got %d", len(body.Bids))
	}
	if len(body.Asks) != 1 {
		t.Fatalf("expected 1 ask level, got %d", len(body.Asks))
	}
	if body.Bids[0].Price != 90 {
		t.Errorf("bid price: got %d, want 90", body.Bids[0].Price)
	}
	if body.Asks[0].Price != 110 {
		t.Errorf("ask price: got %d, want 110", body.Asks[0].Price)
	}
}

func TestGetBook_DefaultDepthIs10(t *testing.T) {
	_, mux := testServer(t)

	// Place 15 orders at different prices.
	for i := 1; i <= 15; i++ {
		postOrder(t, mux, map[string]any{
			"side": "BUY", "type": "LIMIT",
			"price": int64(100 - i), "quantity": 1,
		})
	}

	// No depth param — should default to 10.
	w := getBook(t, mux, 0)
	body := decodeBody[orderbook.BookSnapshot](t, w)
	if len(body.Bids) != 10 {
		t.Errorf("expected 10 bid levels (default depth), got %d", len(body.Bids))
	}
}

func TestGetBook_DepthParam_LimitsLevels(t *testing.T) {
	_, mux := testServer(t)

	for i := 1; i <= 10; i++ {
		postOrder(t, mux, map[string]any{
			"side": "BUY", "type": "LIMIT",
			"price": int64(100 - i), "quantity": 1,
		})
	}

	w := getBook(t, mux, 3)
	body := decodeBody[orderbook.BookSnapshot](t, w)
	if len(body.Bids) != 3 {
		t.Errorf("expected 3 bid levels, got %d", len(body.Bids))
	}
}

// ---------------------------------------------------------------------------
// Content-Type header
// ---------------------------------------------------------------------------

func TestAllEndpoints_ReturnJSONContentType(t *testing.T) {
	_, mux := testServer(t)

	cases := []struct {
		method string
		path   string
		body   any
	}{
		{"POST", "/orders", map[string]any{"side": "BUY", "type": "LIMIT", "price": 100, "quantity": 10}},
		{"GET", "/health", nil},
		{"GET", "/book", nil},
	}

	for _, tc := range cases {
		var req *http.Request
		if tc.body != nil {
			b, _ := json.Marshal(tc.body)
			req = httptest.NewRequest(tc.method, tc.path, bytes.NewReader(b))
			req.Header.Set("Content-Type", "application/json")
		} else {
			req = httptest.NewRequest(tc.method, tc.path, nil)
		}
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, req)

		ct := w.Header().Get("Content-Type")
		if ct != "application/json" {
			t.Errorf("%s %s: Content-Type got %q, want \"application/json\"", tc.method, tc.path, ct)
		}
	}
}

// ---------------------------------------------------------------------------
// sendCommand — timeout behaviour
// ---------------------------------------------------------------------------

func TestSendCommand_EngineTimeout_Returns422(t *testing.T) {
	// Fill the command channel so the next send blocks — simulates a stalled engine.
	cmdCh := make(chan orderbook.Command, 1)
	cmdCh <- orderbook.Command{ResultCh: make(chan orderbook.CommandResult, 1)}
	h := api.NewHandler(cmdCh)

	// Cancel the request context immediately so sendCommand bails out
	// rather than waiting the full engineTimeout (2s) in the test.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	req := httptest.NewRequest(http.MethodPost, "/orders", bytes.NewBufferString(`{
		"side": "BUY", "type": "LIMIT", "price": 100, "quantity": 10
	}`))
	req.Header.Set("Content-Type", "application/json")
	req = req.WithContext(ctx)

	w := httptest.NewRecorder()
	h.PlaceOrder(w, req)

	// sendCommand returns an error — handler responds with 422.
	if w.Code != http.StatusUnprocessableEntity {
		t.Errorf("status: got %d, want 422", w.Code)
	}
}
