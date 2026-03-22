// Benchmarks for the matching engine hot path.
//
// Run with:
//   go test -bench=. -benchmem ./internal/orderbook/...
//
// To run a specific benchmark:
//   go test -bench=BenchmarkPlaceOrder_FullMatch -benchmem ./internal/orderbook/...
//
// To profile CPU:
//   go test -bench=BenchmarkPlaceOrder_FullMatch -cpuprofile=cpu.prof ./internal/orderbook/...
//   go tool pprof cpu.prof

package orderbook

import (
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

func benchOrder(id uint64, side Side, orderType OrderType, price, qty int64) *Order {
	return &Order{
		ID:        id,
		Side:      side,
		Type:      orderType,
		Price:     price,
		Quantity:  qty,
		Timestamp: time.Now().UnixNano(),
	}
}

// ---------------------------------------------------------------------------
// Core matching benchmarks
// ---------------------------------------------------------------------------

// BenchmarkPlaceOrder_NoMatch measures the cost of placing a limit order
// that finds no counterparty and rests in the book.
// This is the most common operation in a real book — most orders rest.
func BenchmarkPlaceOrder_NoMatch(b *testing.B) {
	ob := NewOrderBook("BTC/USD")
	b.ResetTimer()
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		order := benchOrder(uint64(i)+1, Buy, LimitOrder, int64(100+i%100), 10)
		ob.PlaceOrder(order)
	}
}

// BenchmarkPlaceOrder_FullMatch measures the cost of placing an order that
// immediately and fully matches against a single resting order.
// Tests the hot path: match + remove from book + generate trade.
func BenchmarkPlaceOrder_FullMatch(b *testing.B) {
	ob := NewOrderBook("BTC/USD")
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		b.StopTimer()
		// Pre-place a resting sell for this iteration.
		sell := benchOrder(uint64(i)*2+1, Sell, LimitOrder, 100, 10)
		ob.PlaceOrder(sell)
		b.StartTimer()

		// Measure only the matching buy.
		buy := benchOrder(uint64(i)*2+2, Buy, LimitOrder, 100, 10)
		ob.PlaceOrder(buy)
	}
}

// BenchmarkPlaceOrder_MultiLevelMatch measures matching across multiple
// price levels — the aggressor consumes 5 levels in one PlaceOrder call.
// Tests the loop overhead and level cleanup path.
func BenchmarkPlaceOrder_MultiLevelMatch(b *testing.B) {
	const levels = 5
	ob := NewOrderBook("BTC/USD")
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		b.StopTimer()
		// Pre-place resting sells at 5 price levels.
		for j := 0; j < levels; j++ {
			sell := benchOrder(uint64(i*levels+j+1), Sell, LimitOrder, int64(100+j), 2)
			ob.PlaceOrder(sell)
		}
		b.StartTimer()

		// Aggressor consumes all 5 levels.
		buy := benchOrder(uint64(i*levels+levels+1), Buy, LimitOrder, int64(100+levels), 10)
		ob.PlaceOrder(buy)
	}
}

// BenchmarkCancelOrder measures the cost of cancelling a resting order.
// This exercises the orderIndex O(1) lookup and intrusive list O(1) removal.
func BenchmarkCancelOrder(b *testing.B) {
	ob := NewOrderBook("BTC/USD")

	// Pre-fill the book with b.N orders so each cancel iteration has something to cancel.
	orders := make([]*Order, b.N)
	for i := 0; i < b.N; i++ {
		orders[i] = benchOrder(uint64(i+1), Buy, LimitOrder, int64(100+i%1000), 10)
		ob.PlaceOrder(orders[i])
	}

	b.ResetTimer()
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		ob.CancelOrder(orders[i].ID)
	}
}

// BenchmarkNewID measures the Snowflake ID generator throughput.
// This is called on every PlaceOrder and every Trade — it must be fast.
func BenchmarkNewID(b *testing.B) {
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		NewID()
	}
}

// BenchmarkNewID_Parallel measures Snowflake throughput under concurrent load.
// Real API handlers call NewID from multiple goroutines simultaneously.
func BenchmarkNewID_Parallel(b *testing.B) {
	b.ReportAllocs()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			NewID()
		}
	})
}

// ---------------------------------------------------------------------------
// Book state benchmarks
// ---------------------------------------------------------------------------

// BenchmarkBestBid measures the cost of reading the best bid.
// Called on every match attempt — must be O(1).
func BenchmarkBestBid(b *testing.B) {
	ob := NewOrderBook("BTC/USD")
	for i := 0; i < 100; i++ {
		ob.PlaceOrder(benchOrder(uint64(i+1), Buy, LimitOrder, int64(100+i), 10))
	}
	b.ResetTimer()
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		ob.BestBid()
	}
}

// BenchmarkBidLevels measures the cost of taking a depth-5 book snapshot.
// Called after every PlaceOrder and CancelOrder for WebSocket broadcast.
func BenchmarkBidLevels(b *testing.B) {
	ob := NewOrderBook("BTC/USD")
	for i := 0; i < 20; i++ {
		ob.PlaceOrder(benchOrder(uint64(i+1), Buy, LimitOrder, int64(100+i), 10))
	}
	b.ResetTimer()
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		ob.BidLevels(5)
	}
}

// ---------------------------------------------------------------------------
// Throughput benchmark
// ---------------------------------------------------------------------------

// BenchmarkThroughput_AlternatingOrders measures sustained throughput when
// buy and sell orders alternate — every order immediately matches.
// This is the maximum-load scenario: every PlaceOrder generates a trade.
//
// The reported ns/op is the average latency per matched order pair.
// Divide 1,000,000,000 by ns/op to get orders per second.
func BenchmarkThroughput_AlternatingOrders(b *testing.B) {
	ob := NewOrderBook("BTC/USD")
	b.ReportAllocs()
	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		sell := benchOrder(uint64(i*2+1), Sell, LimitOrder, 100, 10)
		ob.PlaceOrder(sell)
		buy := benchOrder(uint64(i*2+2), Buy, LimitOrder, 100, 10)
		ob.PlaceOrder(buy)
	}
}
