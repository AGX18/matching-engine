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
