// The Engine is the guardian of the OrderBook.
//
// Architecture principle:
//   The OrderBook itself has NO locks — it is not goroutine-safe by design.
//   Instead, ALL mutations flow through a single unbuffered command channel.
//   The engine's Run() goroutine is the ONLY code that ever touches the book.
//   This gives us sequential, deterministic processing with zero lock contention.
//
// This results in lower latency than mutex-based designs because there is no
// lock acquisition overhead on the hot path.

package engine

import (
	"fmt"

	"github.com/agx18/matching-engine/internal/orderbook"
)

type EventType int8

const (
	EventTrade      EventType = 0
	EventBookUpdate EventType = 1
)

// TradeEvent is what gets broadcast over WebSocket after each match.
type TradeEvent struct {
	Type       EventType        `json:"type"`
	Trade      *orderbook.Trade `json:"trade,omitempty"`
	BookUpdate *BookUpdateEvent `json:"book_update,omitempty"`
	Timestamp  int64            `json:"timestamp"` // Unix nanos
}

// BookUpdateEvent is a partial snapshot of the book after each change.
type BookUpdateEvent struct {
	Symbol string                         `json:"symbol"`
	Bids   []orderbook.PriceLevelSnapshot `json:"bids"`
	Asks   []orderbook.PriceLevelSnapshot `json:"asks"`
}

// Engine wraps the OrderBook and provides a channel-based interface.
type Engine struct {
	book      *orderbook.OrderBook
	commandCh chan orderbook.Command // incoming commands from API handlers
	eventCh   chan TradeEvent        // outgoing events to WebSocket broadcaster
	done      chan struct{}          // signals shutdown
}

// New creates an Engine for the given symbol.
// commandBuf: size of the command channel buffer. Use 0 for back-pressure (recommended).
// eventBuf:   size of the event channel buffer. Tune based on subscriber count.
func New(symbol string, commandBuf, eventBuf int) *Engine {
	return &Engine{
		book:      orderbook.NewOrderBook(symbol),
		commandCh: make(chan orderbook.Command, commandBuf),
		eventCh:   make(chan TradeEvent, eventBuf),
		done:      make(chan struct{}),
	}
}

// Submit sends a command to the engine. This is safe to call from any goroutine.
// It blocks until the engine receives the command (back-pressure by design).
func (e *Engine) Submit(cmd orderbook.Command) {
	e.commandCh <- cmd
}

// Events returns the read-only event channel for the broadcaster to consume.
func (e *Engine) Events() <-chan TradeEvent {
	return e.eventCh
}

// CommandCh returns a send-only reference for the API layer.
func (e *Engine) CommandCh() chan<- orderbook.Command {
	return e.commandCh
}

// Shutdown signals the engine loop to stop.
func (e *Engine) Shutdown() {
	close(e.done)
}

func (e EventType) MarshalJSON() ([]byte, error) {
	switch e {
	case EventTrade:
		return []byte(`"TRADE"`), nil
	case EventBookUpdate:
		return []byte(`"BOOK_UPDATE"`), nil
	default:
		return nil, fmt.Errorf("unknown event type: %d", e)
	}
}

func (e EventType) String() string {
	switch e {
	case EventTrade:
		return "TRADE"
	case EventBookUpdate:
		return "BOOK_UPDATE"
	default:
		return fmt.Sprintf("UNKNOWN(%d)", e)
	}
}
