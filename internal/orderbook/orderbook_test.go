package orderbook

import (
	"testing"
	"time"
)

// mockOrder is a helper to quickly generate boilerplate limit orders for testing.
func mockOrder(id uint64, side Side, price, qty int64) *Order {
	return &Order{
		ID:        id,
		Side:      side,
		Type:      LimitOrder,
		Price:     price,
		Quantity:  qty,
		Filled:    0,
		Status:    StatusOpen,
		Timestamp: time.Now().UnixNano(),
		ClientID:  999,
	}
}

func TestOrderBook_BestPrices(t *testing.T) {
	ob := NewOrderBook("BTC/USD")

	// 1. Initial state should be 0
	if ob.BestBid() != 0 {
		t.Fatalf("expected BestBid to be 0, got %d", ob.BestBid())
	}
	if ob.BestAsk() != 0 {
		t.Fatalf("expected BestAsk to be 0, got %d", ob.BestAsk())
	}

	// 2. Add bids and verify sorting (highest price first)
	_ = ob.AddOrder(mockOrder(1, Buy, 60000, 10))
	_ = ob.AddOrder(mockOrder(2, Buy, 60100, 10)) // New best bid
	_ = ob.AddOrder(mockOrder(3, Buy, 59900, 10))

	if ob.BestBid() != 60100 {
		t.Errorf("expected BestBid to be 60100, got %d", ob.BestBid())
	}

	// 3. Add asks and verify sorting (lowest price first)
	_ = ob.AddOrder(mockOrder(4, Sell, 60500, 10))
	_ = ob.AddOrder(mockOrder(5, Sell, 60400, 10)) // New best ask
	_ = ob.AddOrder(mockOrder(6, Sell, 60600, 10))

	if ob.BestAsk() != 60400 {
		t.Errorf("expected BestAsk to be 60400, got %d", ob.BestAsk())
	}
}

func TestOrderBook_PriceLevelAggregation(t *testing.T) {
	ob := NewOrderBook("BTC/USD")

	// Add 3 orders at the exact same price level
	_ = ob.AddOrder(mockOrder(1, Buy, 60000, 10))
	_ = ob.AddOrder(mockOrder(2, Buy, 60000, 15))
	_ = ob.AddOrder(mockOrder(3, Buy, 60000, 5))

	snaps := ob.BidLevels(5)

	if len(snaps) != 1 {
		t.Fatalf("expected exactly 1 bid level, got %d", len(snaps))
	}

	level := snaps[0]
	if level.Price != 60000 {
		t.Errorf("expected price 60000, got %d", level.Price)
	}
	if level.Orders != 3 {
		t.Errorf("expected 3 orders at this level, got %d", level.Orders)
	}
	if level.Quantity != 30 {
		t.Errorf("expected total quantity of 30, got %d", level.Quantity)
	}
}

func TestOrderBook_CancelOrder_And_LevelCleanup(t *testing.T) {
	ob := NewOrderBook("BTC/USD")

	_ = ob.AddOrder(mockOrder(1, Buy, 60000, 10))
	_ = ob.AddOrder(mockOrder(2, Buy, 60000, 15))

	// Cancel the first order
	cancelledOrder, err := ob.CancelOrder(1)
	if err != nil {
		t.Fatalf("unexpected error cancelling order: %v", err)
	}
	if cancelledOrder.Status != StatusCancelled {
		t.Errorf("expected order status to be Cancelled")
	}

	// Verify the level updated correctly but still exists
	snaps := ob.BidLevels(1)
	if snaps[0].Orders != 1 {
		t.Errorf("expected 1 order remaining, got %d", snaps[0].Orders)
	}
	if snaps[0].Quantity != 15 {
		t.Errorf("expected 15 quantity remaining, got %d", snaps[0].Quantity)
	}

	// Cancel the final order at this level
	_, _ = ob.CancelOrder(2)

	// Verify the price level was completely deleted
	if ob.BestBid() != 0 {
		t.Errorf("expected BestBid to be 0 (level deleted), got %d", ob.BestBid())
	}
	if len(ob.BidLevels(1)) != 0 {
		t.Errorf("expected no bid levels remaining")
	}
}

func TestOrderBook_ErrorCases(t *testing.T) {
	ob := NewOrderBook("BTC/USD")

	t.Run("Market Order Rejection", func(t *testing.T) {
		mo := mockOrder(1, Buy, 0, 10)
		mo.Type = MarketOrder
		err := ob.AddOrder(mo)
		if err == nil {
			t.Error("expected error when adding market order to book, got nil")
		}
	})

	t.Run("Duplicate Order ID Rejection", func(t *testing.T) {
		o := mockOrder(2, Buy, 60000, 10)
		_ = ob.AddOrder(o)
		err := ob.AddOrder(o)
		if err == nil {
			t.Error("expected error when adding duplicate order ID, got nil")
		}
	})

	t.Run("Cancel Non-Existent Order", func(t *testing.T) {
		_, err := ob.CancelOrder(999)
		if err == nil {
			t.Error("expected error when cancelling non-existent order, got nil")
		}
	})
}
