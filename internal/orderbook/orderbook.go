// internal/orderbook/book.go
//
// The Limit Order Book (LOB).
//
// Data structures:
//   - bids/asks: map[price]→PriceLevel for O(1) level access.
//   - bidPrices/askPrices: sorted []int64 for O(log n) best-price maintenance.
//   - PriceLevel: intrusive doubly-linked list (FIFO queue) for O(1) add/remove.
//   - orderIndex: map[uint64]→*Order for O(1) cancel lookups.
//
// None of this is goroutine-safe. All calls must come from the engine goroutine.

package orderbook

import (
	"fmt"
	"sort"
)

// ---------------------------------------------------------------------------
// PriceLevel — intrusive doubly-linked list
// ---------------------------------------------------------------------------

type PriceLevel struct {
	Price int64
	head  *Order // oldest order — matched first (FIFO)
	tail  *Order // newest order — appended here
	size  int
	total int64 // sum of Remaining() across all orders; maintained by fill/cancel/push
}

// push appends an order to the tail of the FIFO queue. O(1).
func (pl *PriceLevel) push(o *Order) {
	o.prev = pl.tail
	o.next = nil
	if pl.tail != nil {
		pl.tail.next = o
	} else {
		pl.head = o
	}
	pl.tail = o
	pl.size++
	pl.total += o.Remaining()
}

// detach rewires pointers to remove an order from the list. O(1).
// Private — callers must go through fill or cancel, never detach directly.
// detach does not touch total, size, Filled, or Status — those belong
// to fill and cancel, which have the context to update them correctly.
func (pl *PriceLevel) detach(o *Order) {
	if o.prev != nil {
		o.prev.next = o.next
	} else {
		pl.head = o.next
	}
	if o.next != nil {
		o.next.prev = o.prev
	} else {
		pl.tail = o.prev
	}
	o.prev = nil
	o.next = nil
}

// fill applies a match of qty units against a resting order.
// Owns all side effects: total, Filled, Status, and removal if fully consumed.
// This is the only method matching.go needs to call on a PriceLevel —
// no state leaks out for the caller to manage.
func (pl *PriceLevel) fill(o *Order, qty int64) {
	if qty <= 0 || qty > o.Remaining() {
		panic(fmt.Sprintf("fill: invalid qty %d for order %d with remaining %d",
			qty, o.ID, o.Remaining()))
	}
	pl.total -= qty
	o.Filled += qty
	if o.Remaining() == 0 {
		o.Status = StatusFilled
		pl.detach(o)
		pl.size--
	} else {
		o.Status = StatusPartial
	}
}

// cancel removes a resting order and deducts its remaining quantity from total.
// The only method CancelOrder needs to call — Status is set here, not by the caller.
func (pl *PriceLevel) cancel(o *Order) {
	pl.total -= o.Remaining()
	o.Status = StatusCancelled
	pl.detach(o)
	pl.size--
}

func (pl *PriceLevel) isEmpty() bool {
	return pl.head == nil
}

// ---------------------------------------------------------------------------
// OrderBook
// ---------------------------------------------------------------------------

type OrderBook struct {
	Symbol string

	// bids: price → PriceLevel, prices kept sorted descending (best bid first)
	bids      map[int64]*PriceLevel
	bidPrices []int64

	// asks: price → PriceLevel, prices kept sorted ascending (best ask first)
	asks      map[int64]*PriceLevel
	askPrices []int64

	// orderIndex: O(1) lookup by ID for cancellations.
	// Key is uint64 — hashed as a single integer, not a byte-by-byte string scan.
	orderIndex map[uint64]*Order
}

func NewOrderBook(symbol string) *OrderBook {
	return &OrderBook{
		Symbol:     symbol,
		bids:       make(map[int64]*PriceLevel),
		asks:       make(map[int64]*PriceLevel),
		orderIndex: make(map[uint64]*Order),
	}
}

// ---------------------------------------------------------------------------
// Sorted price list helpers
// ---------------------------------------------------------------------------

func (ob *OrderBook) insertBidPrice(p int64) {
	i := sort.Search(len(ob.bidPrices), func(i int) bool { return ob.bidPrices[i] <= p })
	ob.bidPrices = append(ob.bidPrices, 0)
	copy(ob.bidPrices[i+1:], ob.bidPrices[i:])
	ob.bidPrices[i] = p
}

func (ob *OrderBook) insertAskPrice(p int64) {
	i := sort.Search(len(ob.askPrices), func(i int) bool { return ob.askPrices[i] >= p })
	ob.askPrices = append(ob.askPrices, 0)
	copy(ob.askPrices[i+1:], ob.askPrices[i:])
	ob.askPrices[i] = p
}

func (ob *OrderBook) removeBidPrice(p int64) {
	i := sort.Search(len(ob.bidPrices), func(i int) bool { return ob.bidPrices[i] <= p })
	if i < len(ob.bidPrices) && ob.bidPrices[i] == p {
		ob.bidPrices = append(ob.bidPrices[:i], ob.bidPrices[i+1:]...)
	}
}

func (ob *OrderBook) removeAskPrice(p int64) {
	i := sort.Search(len(ob.askPrices), func(i int) bool { return ob.askPrices[i] >= p })
	if i < len(ob.askPrices) && ob.askPrices[i] == p {
		ob.askPrices = append(ob.askPrices[:i], ob.askPrices[i+1:]...)
	}
}

// ---------------------------------------------------------------------------
// Public API
// ---------------------------------------------------------------------------

// AddOrder places a resting limit order into the book.
// Must only be called after matching has determined no immediate fill.
func (ob *OrderBook) AddOrder(order *Order) error {
	if order.Type == MarketOrder {
		return fmt.Errorf("market orders cannot rest in the book")
	}
	if _, exists := ob.orderIndex[order.ID]; exists {
		return fmt.Errorf("order %d already in book", order.ID)
	}
	ob.orderIndex[order.ID] = order

	if order.Side == Buy {
		if _, exists := ob.bids[order.Price]; !exists {
			ob.bids[order.Price] = &PriceLevel{Price: order.Price}
			ob.insertBidPrice(order.Price)
		}
		ob.bids[order.Price].push(order)
	} else {
		if _, exists := ob.asks[order.Price]; !exists {
			ob.asks[order.Price] = &PriceLevel{Price: order.Price}
			ob.insertAskPrice(order.Price)
		}
		ob.asks[order.Price].push(order)
	}
	return nil
}

// CancelOrder removes a resting order by its uint64 ID.
// O(1): one map lookup + O(1) linked list removal.
func (ob *OrderBook) CancelOrder(orderID uint64) (*Order, error) {
	order, exists := ob.orderIndex[orderID]
	if !exists {
		return nil, fmt.Errorf("order %d not found", orderID)
	}
	if !order.IsActive() {
		return nil, fmt.Errorf("order %d is not active (status: %d)", orderID, order.Status)
	}

	delete(ob.orderIndex, orderID)
	ob.cancelFromLevel(order) // sets Status = StatusCancelled internally
	return order, nil
}

// cancelFromLevel calls level.cancel and cleans up the price level if empty.
func (ob *OrderBook) cancelFromLevel(order *Order) {
	if order.Side == Buy {
		level, ok := ob.bids[order.Price]
		if !ok {
			return
		}
		level.cancel(order) // owns total, Status, detach, size
		if level.isEmpty() {
			delete(ob.bids, order.Price)
			ob.removeBidPrice(order.Price)
		}
	} else {
		level, ok := ob.asks[order.Price]
		if !ok {
			return
		}
		level.cancel(order)
		if level.isEmpty() {
			delete(ob.asks, order.Price)
			ob.removeAskPrice(order.Price)
		}
	}
}

// BestBid returns the highest resting buy price, or 0 if empty.
func (ob *OrderBook) BestBid() int64 {
	if len(ob.bidPrices) == 0 {
		return 0
	}
	return ob.bidPrices[0]
}

// BestAsk returns the lowest resting sell price, or 0 if empty.
func (ob *OrderBook) BestAsk() int64 {
	if len(ob.askPrices) == 0 {
		return 0
	}
	return ob.askPrices[0]
}

// ---------------------------------------------------------------------------
// Snapshot (for REST / WebSocket responses)
// ---------------------------------------------------------------------------

type PriceLevelSnapshot struct {
	Price    int64 `json:"price"`
	Quantity int64 `json:"quantity"`
	Orders   int   `json:"orders"`
}

func (ob *OrderBook) BidLevels(depth int) []PriceLevelSnapshot {
	return ob.snapshot(ob.bidPrices, ob.bids, depth)
}

func (ob *OrderBook) AskLevels(depth int) []PriceLevelSnapshot {
	return ob.snapshot(ob.askPrices, ob.asks, depth)
}

func (ob *OrderBook) snapshot(prices []int64, levels map[int64]*PriceLevel, depth int) []PriceLevelSnapshot {
	if depth <= 0 || depth > len(prices) {
		depth = len(prices)
	}
	result := make([]PriceLevelSnapshot, 0, depth)
	for i := 0; i < depth; i++ {
		p := prices[i]
		lvl := levels[p]
		result = append(result, PriceLevelSnapshot{
			Price:    p,
			Quantity: lvl.total,
			Orders:   lvl.size,
		})
	}
	return result
}
