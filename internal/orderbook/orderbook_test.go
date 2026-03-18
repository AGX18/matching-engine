package orderbook

import (
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// newOrder builds a test order with sensible defaults.
// price and qty use raw int64 — in tests we use small round numbers
// rather than fixed-point to keep assertions readable.
func newOrder(id uint64, side Side, orderType OrderType, price, qty int64) *Order {
	return &Order{
		ID:        id,
		Side:      side,
		Type:      orderType,
		Price:     price,
		Quantity:  qty,
		Timestamp: time.Now().UnixNano(),
	}
}

func newBuyLimit(id uint64, price, qty int64) *Order {
	return newOrder(id, Buy, LimitOrder, price, qty)
}

func newSellLimit(id uint64, price, qty int64) *Order {
	return newOrder(id, Sell, LimitOrder, price, qty)
}

func newBuyMarket(id uint64, qty int64) *Order {
	return newOrder(id, Buy, MarketOrder, 0, qty)
}

func newSellMarket(id uint64, qty int64) *Order {
	return newOrder(id, Sell, MarketOrder, 0, qty)
}

func newBook() *OrderBook {
	return NewOrderBook("BTC/USD")
}

// assertTrade checks a trade's fields match expectations.
func assertTrade(t *testing.T, trade *Trade, wantPrice, wantQty int64, wantBuyID, wantSellID uint64) {
	t.Helper()
	if trade.Price != wantPrice {
		t.Errorf("trade price: got %d, want %d", trade.Price, wantPrice)
	}
	if trade.Quantity != wantQty {
		t.Errorf("trade quantity: got %d, want %d", trade.Quantity, wantQty)
	}
	if trade.BuyOrderID != wantBuyID {
		t.Errorf("trade BuyOrderID: got %d, want %d", trade.BuyOrderID, wantBuyID)
	}
	if trade.SellOrderID != wantSellID {
		t.Errorf("trade SellOrderID: got %d, want %d", trade.SellOrderID, wantSellID)
	}
}

// ---------------------------------------------------------------------------
// PlaceOrder — input validation
// ---------------------------------------------------------------------------

func TestPlaceOrder_RejectsZeroQuantity(t *testing.T) {
	ob := newBook()
	order := newBuyLimit(1, 100, 0) // qty = 0

	_, err := ob.PlaceOrder(order)
	if err == nil {
		t.Fatal("expected error for zero quantity, got nil")
	}
}

func TestPlaceOrder_RejectsNegativeQuantity(t *testing.T) {
	ob := newBook()
	order := newBuyLimit(1, 100, -10)

	_, err := ob.PlaceOrder(order)
	if err == nil {
		t.Fatal("expected error for negative quantity, got nil")
	}
}

func TestPlaceOrder_RejectsZeroPriceForLimitOrder(t *testing.T) {
	ob := newBook()
	order := newBuyLimit(1, 0, 10)

	_, err := ob.PlaceOrder(order)
	if err == nil {
		t.Fatal("expected error for zero limit price, got nil")
	}
}

func TestPlaceOrder_MarketOrderDoesNotRequirePrice(t *testing.T) {
	ob := newBook()
	// Add a resting sell so the market order has something to match against.
	ob.PlaceOrder(newSellLimit(1, 100, 10))

	order := newBuyMarket(2, 5)
	_, err := ob.PlaceOrder(order)
	if err != nil {
		t.Fatalf("unexpected error for market order with no price: %v", err)
	}
}

// ---------------------------------------------------------------------------
// Resting orders — no match available
// ---------------------------------------------------------------------------

func TestLimitOrder_RestsWhenNoCounterparty(t *testing.T) {
	ob := newBook()
	buy := newBuyLimit(1, 100, 10)

	trades, err := ob.PlaceOrder(buy)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(trades) != 0 {
		t.Fatalf("expected no trades, got %d", len(trades))
	}
	if buy.Status != StatusOpen {
		t.Errorf("expected StatusOpen, got %d", buy.Status)
	}
	if ob.BestBid() != 100 {
		t.Errorf("expected best bid 100, got %d", ob.BestBid())
	}
}

func TestMarketOrder_DiscardedWhenBookEmpty(t *testing.T) {
	ob := newBook()
	buy := newBuyMarket(1, 10)

	trades, err := ob.PlaceOrder(buy)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(trades) != 0 {
		t.Fatalf("expected no trades, got %d", len(trades))
	}
	// Market orders never rest — book should be empty.
	if ob.BestBid() != 0 {
		t.Errorf("market order should not rest in book, BestBid = %d", ob.BestBid())
	}
}

// ---------------------------------------------------------------------------
// Full match — one buy, one sell, exact quantities
// ---------------------------------------------------------------------------

func TestFullMatch_BuyAgainstRestingSell(t *testing.T) {
	ob := newBook()
	sell := newSellLimit(1, 100, 10)
	ob.PlaceOrder(sell)

	buy := newBuyLimit(2, 100, 10)
	trades, err := ob.PlaceOrder(buy)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(trades) != 1 {
		t.Fatalf("expected 1 trade, got %d", len(trades))
	}
	assertTrade(t, trades[0], 100, 10, 2, 1)

	if buy.Status != StatusFilled {
		t.Errorf("buy status: got %d, want StatusFilled", buy.Status)
	}
	if sell.Status != StatusFilled {
		t.Errorf("sell status: got %d, want StatusFilled", sell.Status)
	}
	// Book should be empty after full match.
	if ob.BestBid() != 0 || ob.BestAsk() != 0 {
		t.Error("book should be empty after full match")
	}
}

func TestFullMatch_SellAgainstRestingBuy(t *testing.T) {
	ob := newBook()
	buy := newBuyLimit(1, 100, 10)
	ob.PlaceOrder(buy)

	sell := newSellLimit(2, 100, 10)
	trades, err := ob.PlaceOrder(sell)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(trades) != 1 {
		t.Fatalf("expected 1 trade, got %d", len(trades))
	}
	assertTrade(t, trades[0], 100, 10, 1, 2)
}

// ---------------------------------------------------------------------------
// Partial match
// ---------------------------------------------------------------------------

func TestPartialMatch_AggressorPartiallyFilled(t *testing.T) {
	ob := newBook()
	// Resting sell for 5 units.
	ob.PlaceOrder(newSellLimit(1, 100, 5))

	// Buy for 10 units — only 5 available.
	buy := newBuyLimit(2, 100, 10)
	trades, err := ob.PlaceOrder(buy)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(trades) != 1 {
		t.Fatalf("expected 1 trade, got %d", len(trades))
	}
	assertTrade(t, trades[0], 100, 5, 2, 1)

	if buy.Status != StatusPartial {
		t.Errorf("buy status: got %d, want StatusPartial", buy.Status)
	}
	if buy.Remaining() != 5 {
		t.Errorf("buy remaining: got %d, want 5", buy.Remaining())
	}
	// Remaining 5 units should be resting in the book.
	if ob.BestBid() != 100 {
		t.Errorf("expected buy to rest at 100, got %d", ob.BestBid())
	}
}

func TestPartialMatch_RestingOrderPartiallyFilled(t *testing.T) {
	ob := newBook()
	// Resting sell for 10 units.
	sell := newSellLimit(1, 100, 10)
	ob.PlaceOrder(sell)

	// Buy for only 3 units.
	buy := newBuyLimit(2, 100, 3)
	trades, _ := ob.PlaceOrder(buy)

	if len(trades) != 1 {
		t.Fatalf("expected 1 trade, got %d", len(trades))
	}
	assertTrade(t, trades[0], 100, 3, 2, 1)

	if sell.Status != StatusPartial {
		t.Errorf("sell status: got %d, want StatusPartial", sell.Status)
	}
	if sell.Remaining() != 7 {
		t.Errorf("sell remaining: got %d, want 7", sell.Remaining())
	}
	// Sell should still be in the book with 7 remaining.
	if ob.BestAsk() != 100 {
		t.Errorf("expected sell to remain at 100, got %d", ob.BestAsk())
	}
}

// ---------------------------------------------------------------------------
// Price-Time Priority
// ---------------------------------------------------------------------------

func TestPricePriority_HigherBidMatchesFirst(t *testing.T) {
	ob := newBook()
	// Two buys at different prices — higher price should match first.
	ob.PlaceOrder(newBuyLimit(1, 90, 5))  // lower price
	ob.PlaceOrder(newBuyLimit(2, 100, 5)) // higher price — should match first

	sell := newSellLimit(3, 90, 5)
	trades, _ := ob.PlaceOrder(sell)

	if len(trades) != 1 {
		t.Fatalf("expected 1 trade, got %d", len(trades))
	}
	// Should match against order 2 (price 100), not order 1 (price 90).
	if trades[0].BuyOrderID != 2 {
		t.Errorf("expected match against order 2 (higher price), got order %d", trades[0].BuyOrderID)
	}
}

func TestPricePriority_LowerAskMatchesFirst(t *testing.T) {
	ob := newBook()
	// Two sells at different prices — lower price should match first.
	ob.PlaceOrder(newSellLimit(1, 110, 5)) // higher price
	ob.PlaceOrder(newSellLimit(2, 100, 5)) // lower price — should match first

	buy := newBuyLimit(3, 110, 5)
	trades, _ := ob.PlaceOrder(buy)

	if len(trades) != 1 {
		t.Fatalf("expected 1 trade, got %d", len(trades))
	}
	// Should match against order 2 (price 100), not order 1 (price 110).
	if trades[0].SellOrderID != 2 {
		t.Errorf("expected match against order 2 (lower price), got order %d", trades[0].SellOrderID)
	}
}

func TestTimePriority_EarliestOrderMatchesFirst(t *testing.T) {
	ob := newBook()
	// Two sells at the same price — first arrival should match first.
	first := newSellLimit(1, 100, 5)
	first.Timestamp = 1000
	second := newSellLimit(2, 100, 5)
	second.Timestamp = 2000

	ob.PlaceOrder(first)
	ob.PlaceOrder(second)

	buy := newBuyLimit(3, 100, 5)
	trades, _ := ob.PlaceOrder(buy)

	if len(trades) != 1 {
		t.Fatalf("expected 1 trade, got %d", len(trades))
	}
	// Should match against order 1 (arrived first).
	if trades[0].SellOrderID != 1 {
		t.Errorf("expected match against order 1 (first arrival), got order %d", trades[0].SellOrderID)
	}
}

// ---------------------------------------------------------------------------
// Trade executes at maker (resting) price
// ---------------------------------------------------------------------------

func TestTradePrice_ExecutesAtRestingPrice(t *testing.T) {
	ob := newBook()
	// Resting sell at 100.
	ob.PlaceOrder(newSellLimit(1, 100, 10))

	// Aggressive buy willing to pay up to 110.
	buy := newBuyLimit(2, 110, 10)
	trades, _ := ob.PlaceOrder(buy)

	if len(trades) != 1 {
		t.Fatalf("expected 1 trade, got %d", len(trades))
	}
	// Trade must execute at the resting order's price (100), not the aggressor's (110).
	if trades[0].Price != 100 {
		t.Errorf("trade price: got %d, want 100 (resting price)", trades[0].Price)
	}
}

// ---------------------------------------------------------------------------
// Multi-level matching
// ---------------------------------------------------------------------------

func TestMultiLevelMatch_AgressorConsumesMultipleLevels(t *testing.T) {
	ob := newBook()
	ob.PlaceOrder(newSellLimit(1, 100, 3))
	ob.PlaceOrder(newSellLimit(2, 101, 3))
	ob.PlaceOrder(newSellLimit(3, 102, 3))

	// Buy enough to consume all three levels.
	buy := newBuyLimit(4, 102, 9)
	trades, _ := ob.PlaceOrder(buy)

	if len(trades) != 3 {
		t.Fatalf("expected 3 trades, got %d", len(trades))
	}
	// First trade at best ask (100).
	if trades[0].Price != 100 {
		t.Errorf("first trade price: got %d, want 100", trades[0].Price)
	}
	// Second trade at next level (101).
	if trades[1].Price != 101 {
		t.Errorf("second trade price: got %d, want 101", trades[1].Price)
	}
	// Third trade at next level (102).
	if trades[2].Price != 102 {
		t.Errorf("third trade price: got %d, want 102", trades[2].Price)
	}

	if buy.Status != StatusFilled {
		t.Errorf("buy status: got %d, want StatusFilled", buy.Status)
	}
	// Book should be empty.
	if ob.BestAsk() != 0 {
		t.Errorf("expected empty ask side, got %d", ob.BestAsk())
	}
}

// ---------------------------------------------------------------------------
// Market orders
// ---------------------------------------------------------------------------

func TestMarketOrder_FullyFillsAgainstBook(t *testing.T) {
	ob := newBook()
	ob.PlaceOrder(newSellLimit(1, 100, 10))

	buy := newBuyMarket(2, 10)
	trades, err := ob.PlaceOrder(buy)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(trades) != 1 {
		t.Fatalf("expected 1 trade, got %d", len(trades))
	}
	if buy.Status != StatusFilled {
		t.Errorf("buy status: got %d, want StatusFilled", buy.Status)
	}
}

func TestMarketOrder_NeverRestsInBook(t *testing.T) {
	ob := newBook()
	// Only 5 units available but market order wants 10.
	ob.PlaceOrder(newSellLimit(1, 100, 5))

	buy := newBuyMarket(2, 10)
	ob.PlaceOrder(buy)

	// Remaining 5 units should NOT rest in the book.
	if ob.BestBid() != 0 {
		t.Errorf("market order should not rest in book, got BestBid = %d", ob.BestBid())
	}
}

func TestMarketOrder_DoesNotMatchBelowLimitPrice(t *testing.T) {
	ob := newBook()
	// Limit buy at 90, sell comes in at 100 — should NOT match.
	ob.PlaceOrder(newBuyLimit(1, 90, 10))

	sell := newSellLimit(2, 100, 10)
	trades, _ := ob.PlaceOrder(sell)

	if len(trades) != 0 {
		t.Fatalf("expected no trades, got %d", len(trades))
	}
}

// ---------------------------------------------------------------------------
// Cancel order
// ---------------------------------------------------------------------------

func TestCancelOrder_RemovesFromBook(t *testing.T) {
	ob := newBook()
	buy := newBuyLimit(1, 100, 10)
	ob.PlaceOrder(buy)

	_, err := ob.CancelOrder(1)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if buy.Status != StatusCancelled {
		t.Errorf("buy status: got %d, want StatusCancelled", buy.Status)
	}
	// Book should be empty after cancel.
	if ob.BestBid() != 0 {
		t.Errorf("expected empty bid side after cancel, got %d", ob.BestBid())
	}
}

func TestCancelOrder_ReturnsErrorForUnknownID(t *testing.T) {
	ob := newBook()

	_, err := ob.CancelOrder(999)
	if err == nil {
		t.Fatal("expected error for unknown order ID, got nil")
	}
}

func TestCancelOrder_ReturnsErrorForAlreadyFilledOrder(t *testing.T) {
	ob := newBook()
	ob.PlaceOrder(newSellLimit(1, 100, 10))

	buy := newBuyLimit(2, 100, 10)
	ob.PlaceOrder(buy) // fully fills sell order 1

	// Try to cancel the already-filled sell order.
	_, err := ob.CancelOrder(1)
	if err == nil {
		t.Fatal("expected error cancelling filled order, got nil")
	}
}

func TestCancelOrder_PartiallyFilledOrderCanBeCancelled(t *testing.T) {
	ob := newBook()
	sell := newSellLimit(1, 100, 10)
	ob.PlaceOrder(sell)

	// Partially fill the sell order.
	ob.PlaceOrder(newBuyLimit(2, 100, 3))

	// Cancel the remaining 7 units.
	_, err := ob.CancelOrder(1)
	if err != nil {
		t.Fatalf("unexpected error cancelling partial order: %v", err)
	}
	if sell.Status != StatusCancelled {
		t.Errorf("sell status: got %d, want StatusCancelled", sell.Status)
	}
	if ob.BestAsk() != 0 {
		t.Errorf("expected empty ask side after cancel, got %d", ob.BestAsk())
	}
}

// ---------------------------------------------------------------------------
// Order book state — BestBid / BestAsk / levels
// ---------------------------------------------------------------------------

func TestBestBid_ReturnsHighestBuyPrice(t *testing.T) {
	ob := newBook()
	ob.PlaceOrder(newBuyLimit(1, 90, 5))
	ob.PlaceOrder(newBuyLimit(2, 100, 5))
	ob.PlaceOrder(newBuyLimit(3, 95, 5))

	if ob.BestBid() != 100 {
		t.Errorf("BestBid: got %d, want 100", ob.BestBid())
	}
}

func TestBestAsk_ReturnsLowestSellPrice(t *testing.T) {
	ob := newBook()
	ob.PlaceOrder(newSellLimit(1, 110, 5))
	ob.PlaceOrder(newSellLimit(2, 100, 5))
	ob.PlaceOrder(newSellLimit(3, 105, 5))

	if ob.BestAsk() != 100 {
		t.Errorf("BestAsk: got %d, want 100", ob.BestAsk())
	}
}

func TestBestBid_ReturnsZeroWhenEmpty(t *testing.T) {
	ob := newBook()
	if ob.BestBid() != 0 {
		t.Errorf("BestBid on empty book: got %d, want 0", ob.BestBid())
	}
}

func TestBidLevels_CorrectDepthAndQuantity(t *testing.T) {
	ob := newBook()
	ob.PlaceOrder(newBuyLimit(1, 100, 5))
	ob.PlaceOrder(newBuyLimit(2, 100, 3)) // same level — total 8
	ob.PlaceOrder(newBuyLimit(3, 90, 10))

	levels := ob.BidLevels(5)
	if len(levels) != 2 {
		t.Fatalf("expected 2 bid levels, got %d", len(levels))
	}
	// First level — best bid.
	if levels[0].Price != 100 {
		t.Errorf("level 0 price: got %d, want 100", levels[0].Price)
	}
	if levels[0].Quantity != 8 {
		t.Errorf("level 0 quantity: got %d, want 8", levels[0].Quantity)
	}
	if levels[0].Orders != 2 {
		t.Errorf("level 0 orders: got %d, want 2", levels[0].Orders)
	}
}

// ---------------------------------------------------------------------------
// PriceLevel total — maintained correctly across fills and cancels
// ---------------------------------------------------------------------------

func TestPriceLevelTotal_UpdatedAfterPartialFill(t *testing.T) {
	ob := newBook()
	ob.PlaceOrder(newSellLimit(1, 100, 10))
	ob.PlaceOrder(newSellLimit(2, 100, 5)) // total at 100 = 15

	// Fill 7 units.
	ob.PlaceOrder(newBuyLimit(3, 100, 7))

	levels := ob.AskLevels(1)
	if len(levels) != 1 {
		t.Fatalf("expected 1 ask level, got %d", len(levels))
	}
	if levels[0].Quantity != 8 { // 15 - 7 = 8
		t.Errorf("level quantity after fill: got %d, want 8", levels[0].Quantity)
	}
}

func TestPriceLevelTotal_UpdatedAfterCancel(t *testing.T) {
	ob := newBook()
	ob.PlaceOrder(newBuyLimit(1, 100, 10))
	ob.PlaceOrder(newBuyLimit(2, 100, 5)) // total = 15

	ob.CancelOrder(1) // cancel 10 units

	levels := ob.BidLevels(1)
	if len(levels) != 1 {
		t.Fatalf("expected 1 bid level, got %d", len(levels))
	}
	if levels[0].Quantity != 5 {
		t.Errorf("level quantity after cancel: got %d, want 5", levels[0].Quantity)
	}
}

func TestPriceLevel_RemovedWhenEmpty(t *testing.T) {
	ob := newBook()
	ob.PlaceOrder(newBuyLimit(1, 100, 10))

	ob.CancelOrder(1)

	if len(ob.BidLevels(10)) != 0 {
		t.Error("expected bid levels to be empty after cancelling last order")
	}
}

// ---------------------------------------------------------------------------
// Snowflake ID — basic properties
// ---------------------------------------------------------------------------

func TestNewID_GeneratesUniqueIDs(t *testing.T) {
	seen := make(map[uint64]bool)
	for i := 0; i < 10_000; i++ {
		id := NewID()
		if seen[id] {
			t.Fatalf("duplicate ID generated: %d", id)
		}
		seen[id] = true
	}
}

func TestNewID_GeneratesMonotonicallyIncreasingIDs(t *testing.T) {
	prev := NewID()
	for i := 0; i < 1000; i++ {
		next := NewID()
		if next <= prev {
			t.Fatalf("ID not monotonically increasing: prev=%d next=%d", prev, next)
		}
		prev = next
	}
}
