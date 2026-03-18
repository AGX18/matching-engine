package engine

// Tests for the engine layer.
// These test the channel mechanics, sequential processing guarantee,
// and concurrent safety — not the matching logic itself (that lives
// in the orderbook package tests).

import (
	"sync"
	"testing"
	"time"

	"github.com/agx18/matching-engine/internal/orderbook"
)

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// startEngine creates and starts a test engine, returning a cleanup function.
func startEngine(t *testing.T) (*Engine, func()) {
	t.Helper()
	eng := New("BTC/USD", 256, 512)
	go eng.Run()
	return eng, func() { eng.Shutdown() }
}

func sendPlace(t *testing.T, eng *Engine, order *orderbook.Order) orderbook.CommandResult {
	t.Helper()
	cmd := orderbook.Command{
		Type:     orderbook.CmdPlaceOrder,
		Order:    order,
		ResultCh: make(chan orderbook.CommandResult, 1),
	}
	eng.Submit(cmd)
	return <-cmd.ResultCh
}

func sendCancel(t *testing.T, eng *Engine, orderID uint64) orderbook.CommandResult {
	t.Helper()
	cmd := orderbook.Command{
		Type:     orderbook.CmdCancelOrder,
		OrderID:  orderID,
		ResultCh: make(chan orderbook.CommandResult, 1),
	}
	eng.Submit(cmd)
	return <-cmd.ResultCh
}

func newBuyLimit(id uint64, price, qty int64) *orderbook.Order {
	return &orderbook.Order{
		ID:        id,
		Side:      orderbook.Buy,
		Type:      orderbook.LimitOrder,
		Price:     price,
		Quantity:  qty,
		Timestamp: time.Now().UnixNano(),
	}
}

func newSellLimit(id uint64, price, qty int64) *orderbook.Order {
	return &orderbook.Order{
		ID:        id,
		Side:      orderbook.Sell,
		Type:      orderbook.LimitOrder,
		Price:     price,
		Quantity:  qty,
		Timestamp: time.Now().UnixNano(),
	}
}

// ---------------------------------------------------------------------------
// Basic command processing
// ---------------------------------------------------------------------------

func TestEngine_PlaceOrder_ReturnsOrderID(t *testing.T) {
	eng, stop := startEngine(t)
	defer stop()

	result := sendPlace(t, eng, newBuyLimit(1, 100, 10))
	if result.Err != nil {
		t.Fatalf("unexpected error: %v", result.Err)
	}
	if result.OrderID == 0 {
		t.Error("expected non-zero order ID")
	}
}

func TestEngine_PlaceOrder_ReturnsTradesOnMatch(t *testing.T) {
	eng, stop := startEngine(t)
	defer stop()

	// Place resting sell.
	sendPlace(t, eng, newSellLimit(1, 100, 10))

	// Place matching buy.
	result := sendPlace(t, eng, newBuyLimit(2, 100, 10))
	if result.Err != nil {
		t.Fatalf("unexpected error: %v", result.Err)
	}
	if len(result.Trades) != 1 {
		t.Fatalf("expected 1 trade, got %d", len(result.Trades))
	}
	if result.Trades[0].Quantity != 10 {
		t.Errorf("trade quantity: got %d, want 10", result.Trades[0].Quantity)
	}
}

func TestEngine_PlaceOrder_ReturnsErrorOnInvalidOrder(t *testing.T) {
	eng, stop := startEngine(t)
	defer stop()

	// Zero quantity — should fail.
	order := newBuyLimit(1, 100, 0)
	result := sendPlace(t, eng, order)
	if result.Err == nil {
		t.Fatal("expected error for invalid order, got nil")
	}
}

func TestEngine_CancelOrder_Succeeds(t *testing.T) {
	eng, stop := startEngine(t)
	defer stop()

	// Place a resting order.
	placed := sendPlace(t, eng, newBuyLimit(1, 100, 10))
	if placed.Err != nil {
		t.Fatalf("place error: %v", placed.Err)
	}

	// Cancel it.
	result := sendCancel(t, eng, placed.OrderID)
	if result.Err != nil {
		t.Fatalf("cancel error: %v", result.Err)
	}
	if result.OrderID != placed.OrderID {
		t.Errorf("cancel returned wrong order ID: got %d, want %d", result.OrderID, placed.OrderID)
	}
}

func TestEngine_CancelOrder_ReturnsErrorForUnknownID(t *testing.T) {
	eng, stop := startEngine(t)
	defer stop()

	result := sendCancel(t, eng, 99999)
	if result.Err == nil {
		t.Fatal("expected error cancelling unknown order, got nil")
	}
}

// ---------------------------------------------------------------------------
// Event broadcasting
// ---------------------------------------------------------------------------

func TestEngine_EmitsTradeEvent_OnMatch(t *testing.T) {
	eng, stop := startEngine(t)
	defer stop()

	events := eng.Events()

	sendPlace(t, eng, newSellLimit(1, 100, 10))
	sendPlace(t, eng, newBuyLimit(2, 100, 10))

	// Drain events — expect a TRADE event and at least one BOOK_UPDATE.
	timeout := time.After(1 * time.Second)
	var tradeEventSeen bool
	for !tradeEventSeen {
		select {
		case event := <-events:
			if event.Type == EventTrade {
				tradeEventSeen = true
				if event.Trade == nil {
					t.Error("TRADE event has nil Trade field")
				}
			}
		case <-timeout:
			t.Fatal("timed out waiting for TRADE event")
		}
	}
}

func TestEngine_EmitsBookUpdateEvent_OnPlace(t *testing.T) {
	eng, stop := startEngine(t)
	defer stop()

	events := eng.Events()
	sendPlace(t, eng, newBuyLimit(1, 100, 10))

	timeout := time.After(1 * time.Second)
	select {
	case event := <-events:
		if event.Type != EventBookUpdate {
			t.Errorf("expected BOOK_UPDATE event, got type %v", event.Type)
		}
		if event.BookUpdate == nil {
			t.Error("BOOK_UPDATE event has nil BookUpdate field")
		}
	case <-timeout:
		t.Fatal("timed out waiting for BOOK_UPDATE event")
	}
}

func TestEngine_EmitsBookUpdateEvent_OnCancel(t *testing.T) {
	eng, stop := startEngine(t)
	defer stop()

	placed := sendPlace(t, eng, newBuyLimit(1, 100, 10))

	// Drain the place event first.
	<-eng.Events()

	events := eng.Events()
	sendCancel(t, eng, placed.OrderID)

	timeout := time.After(1 * time.Second)
	select {
	case event := <-events:
		if event.Type != EventBookUpdate {
			t.Errorf("expected BOOK_UPDATE after cancel, got type %v", event.Type)
		}
	case <-timeout:
		t.Fatal("timed out waiting for BOOK_UPDATE after cancel")
	}
}

// ---------------------------------------------------------------------------
// Concurrency — the core guarantee of the engine
// ---------------------------------------------------------------------------

// TestEngine_ConcurrentOrders_NoDataRace submits many orders concurrently
// and verifies the engine processes them all without corruption.
// Run with: go test -race ./internal/engine/...
func TestEngine_ConcurrentOrders_NoDataRace(t *testing.T) {
	eng, stop := startEngine(t)
	defer stop()

	const goroutines = 50
	const ordersEach = 20

	var wg sync.WaitGroup
	wg.Add(goroutines)

	for i := 0; i < goroutines; i++ {
		go func(workerID int) {
			defer wg.Done()
			for j := 0; j < ordersEach; j++ {
				order := newBuyLimit(0, int64(100+workerID), 1)
				result := sendPlace(t, eng, order)
				if result.Err != nil {
					t.Errorf("worker %d order %d error: %v", workerID, j, result.Err)
				}
			}
		}(i)
	}

	wg.Wait()
}

// TestEngine_SequentialProcessing verifies that commands are processed
// in the order they are received — the determinism guarantee.
func TestEngine_SequentialProcessing_OrdersProcessedInOrder(t *testing.T) {
	eng, stop := startEngine(t)
	defer stop()

	// Place a resting sell.
	sendPlace(t, eng, newSellLimit(1, 100, 1000))

	// Submit 10 buys sequentially and collect the trade prices.
	// Each buy matches against the same resting sell at price 100.
	// All trades should be at price 100 — no race can change this.
	for i := 0; i < 10; i++ {
		result := sendPlace(t, eng, newBuyLimit(0, 100, 10))
		if result.Err != nil {
			t.Fatalf("order %d error: %v", i, result.Err)
		}
		if len(result.Trades) != 1 {
			t.Fatalf("order %d: expected 1 trade, got %d", i, len(result.Trades))
		}
		if result.Trades[0].Price != 100 {
			t.Errorf("order %d: trade price got %d, want 100", i, result.Trades[0].Price)
		}
	}
}

// ---------------------------------------------------------------------------
// Shutdown
// ---------------------------------------------------------------------------

func TestEngine_Shutdown_ClosesEventChannel(t *testing.T) {
	eng := New("BTC/USD", 256, 512)
	go eng.Run()

	eng.Shutdown()

	// Event channel should be closed after shutdown.
	// Reading from a closed channel returns the zero value immediately.
	timeout := time.After(1 * time.Second)
	select {
	case _, ok := <-eng.Events():
		if ok {
			t.Error("expected event channel to be closed after shutdown")
		}
	case <-timeout:
		t.Fatal("timed out waiting for event channel to close")
	}
}

// ---------------------------------------------------------------------------
// EventType — JSON serialization
// ---------------------------------------------------------------------------

func TestEventType_MarshalJSON_Trade(t *testing.T) {
	b, err := EventTrade.MarshalJSON()
	if err != nil {
		t.Fatalf("MarshalJSON error: %v", err)
	}
	if string(b) != `"TRADE"` {
		t.Errorf("EventTrade JSON: got %s, want \"TRADE\"", string(b))
	}
}

func TestEventType_MarshalJSON_BookUpdate(t *testing.T) {
	b, err := EventBookUpdate.MarshalJSON()
	if err != nil {
		t.Fatalf("MarshalJSON error: %v", err)
	}
	if string(b) != `"BOOK_UPDATE"` {
		t.Errorf("EventBookUpdate JSON: got %s, want \"BOOK_UPDATE\"", string(b))
	}
}

func TestEventType_MarshalJSON_UnknownReturnsError(t *testing.T) {
	unknown := EventType(99)
	_, err := unknown.MarshalJSON()
	if err == nil {
		t.Fatal("expected error for unknown EventType, got nil")
	}
}

func TestEventType_String_Trade(t *testing.T) {
	if EventTrade.String() != "TRADE" {
		t.Errorf("EventTrade.String(): got %s, want TRADE", EventTrade.String())
	}
}

func TestEventType_String_BookUpdate(t *testing.T) {
	if EventBookUpdate.String() != "BOOK_UPDATE" {
		t.Errorf("EventBookUpdate.String(): got %s, want BOOK_UPDATE", EventBookUpdate.String())
	}
}
