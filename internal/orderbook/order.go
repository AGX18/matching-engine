// internal/orderbook/order.go
//
// Core domain types. These are the atoms of the entire system.
// Using int64 for price/quantity avoids floating-point non-determinism —
// critical for a matching engine that must produce identical output
// for identical input sequences.

package orderbook

import (
	"runtime"
	"sync/atomic"
	"time"
)

// Side represents which side of the book an order is on.
type Side int8

const (
	Buy  Side = 0
	Sell Side = 1
)

func (s Side) String() string {
	if s == Buy {
		return "BUY"
	}
	return "SELL"
}

// OrderType distinguishes how an order should be handled.
type OrderType int8

const (
	// LimitOrder rests in the book if it cannot be immediately matched.
	LimitOrder OrderType = 0
	// MarketOrder executes immediately at best available price; never rests.
	MarketOrder OrderType = 1
)

// OrderStatus tracks the lifecycle of an order.
//
// Valid transitions:
//
//	StatusOpen → StatusFilled       (fully matched immediately)
//	StatusOpen → StatusPartial      (partially matched, remainder rests)
//	StatusOpen → StatusCancelled    (cancelled before any fill)
//	StatusPartial → StatusFilled    (remainder fully matched later)
//	StatusPartial → StatusCancelled (cancelled after partial fill)
type OrderStatus int8

const (
	StatusOpen OrderStatus = iota
	StatusFilled
	StatusCancelled
	StatusPartial
)

// ---------------------------------------------------------------------------
// Snowflake ID generator
// ---------------------------------------------------------------------------
var snowflakeEpoch = time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC).UnixMilli()

// sequence is atomically incremented to avoid collisions within the same
// millisecond. atomic.Uint64 keeps the generator completely lock-free.
var state atomic.Uint64

// NewID generates a time-ordered, collision-free uint64 ID.
// Safe to call concurrently from multiple goroutines.
func NewID() uint64 {
	for {
		// 1. Read the composite state atomically
		current := state.Load()
		lastMs := current >> 12
		seq := current & 0xFFF

		// 2. Get current time
		nowMs := uint64(time.Now().UnixMilli() - snowflakeEpoch)

		// 3. Evaluate the new state
		if nowMs < lastMs {
			// Clock moved backwards (NTP sync). Spin until it catches up.
			continue
		}

		if nowMs == lastMs {
			// Same millisecond: increment sequence
			seq = (seq + 1) & 0xFFF
			if seq == 0 {
				// We exhausted the 4096 IDs for this millisecond.
				// Spin until the next millisecond ticks over.
				runtime.Gosched()
				continue
			}
		} else {
			// New millisecond: reset sequence to 0
			seq = 0
		}

		// 4. Pack the proposed next state
		nextState := (nowMs << 12) | seq

		// 5. Attempt to commit the new state atomically.
		// If 'state' still equals 'current', it sets it to 'nextState' and returns true.
		// If another goroutine altered 'state' in the meantime, it returns false.
		if state.CompareAndSwap(current, nextState) {
			// Success! Pack it into your specific [41 ms][12 seq][11 unused] format
			return (nowMs << 22) | seq

			// TODO: Add a 10-bit Node/Machine ID for distributed scaling.
			// Currently, this assumes a single-node engine. If multiple API servers
			// generate IDs concurrently, we need to inject a unique Node ID to
			// prevent cross-server collisions.
			// Format would change to: [41 ms][10 node][12 seq]
			// return (nowMs << 22) | (nodeID << 12) | seq
		}

		// CAS failed. Another goroutine beat us. The loop restarts and tries again.
	}
}

// Order is the central entity. Every field is chosen for memory alignment.
// We use int64 for Price and Quantity (representing fixed-point numbers).
// Convention: 1 USD = 1_000_000 units (6 decimal places of precision).
type Order struct {
	// 8-byte fields first — packed with no padding gaps between them.
	Price     int64  // limit price in fixed-point units (0 for market orders)
	Quantity  int64  // total order size in fixed-point units
	Filled    int64  // how much has been matched
	Timestamp int64  // arrival time as Unix nanoseconds; used for time-priority
	ID        uint64 // Snowflake: time-ordered, collision-free, 8 bytes, no heap
	ClientID  uint64 // identifies the submitting client

	// Intrusive doubly-linked list pointers.
	//
	// These connect the order directly into its price level's FIFO queue.
	// Keeping them here — rather than in a separate ListNode wrapper — means
	// the matching engine never needs a second pointer hop to get from the
	// queue position to the order data. Both live in the same struct, so
	// they land on the same cache line. One allocation per order, not two.
	prev *Order
	next *Order

	// 1-byte fields last — minimises the padding the compiler must insert.
	Side   Side
	Type   OrderType
	Status OrderStatus
}

// Remaining returns how much quantity is still open for matching.
func (o *Order) Remaining() int64 {
	return o.Quantity - o.Filled
}

// IsActive returns true if the order can still be matched.
func (o *Order) IsActive() bool {
	return o.Status == StatusOpen || o.Status == StatusPartial
}

// Trade
// ---------------------------------------------------------------------------
//
// An immutable record of a matched event. Created once, never modified.
//
// All ID fields are uint64 to match Order.ID — map lookups and comparisons
// use a single CMP instruction rather than a byte-by-byte string scan.

type Trade struct {
	ID          uint64 // Snowflake ID for this specific trade event
	Price       int64  // execution price — always the resting (maker) order's price
	Quantity    int64  // units exchanged
	BuyOrderID  uint64 // the buy-side order involved in this trade
	SellOrderID uint64 // the sell-side order involved in this trade
	Timestamp   int64  // Unix nanos when the match occurred
}

// ---------------------------------------------------------------------------
// Engine command types
// ---------------------------------------------------------------------------

type CommandType int8

const (
	CmdPlaceOrder  CommandType = 0
	CmdCancelOrder CommandType = 1
)

// Command is the single type flowing through the engine's command channel.
// One type (rather than separate channels per operation) guarantees that
// every command is processed in strict arrival order — critical for
// deterministic output.
type Command struct {
	Order    *Order // set for CmdPlaceOrder
	ResultCh chan CommandResult
	OrderID  uint64 // set for CmdCancelOrder
	Type     CommandType
}

// CommandResult carries the outcome back to the waiting API handler.
// Each handler creates its own ResultCh so concurrent HTTP requests
// never see each other's responses.
type CommandResult struct {
	Trades  []*Trade
	OrderID uint64 // echoed back for client confirmation
	Err     error
}
