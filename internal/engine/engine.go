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
	"log"
	"time"

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
}

// New creates an Engine for the given symbol.
// commandBuf: size of the command channel buffer. Use 0 for back-pressure (recommended).
// eventBuf:   size of the event channel buffer. Tune based on subscriber count.
func New(symbol string, commandBuf, eventBuf int) *Engine {
	return &Engine{
		book:      orderbook.NewOrderBook(symbol),
		commandCh: make(chan orderbook.Command, commandBuf),
		eventCh:   make(chan TradeEvent, eventBuf),
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

// Close shuts down the engine by closing the command channel.
// The Run() loop drains any buffered commands then exits cleanly.
//
// IMPORTANT: the HTTP server must be fully shut down before calling Close()
// to ensure no handler is mid-flight trying to send to commandCh.
// Sending to a closed channel panics — the correct shutdown sequence is:
//  1. srv.Shutdown(ctx)  — drain all in-flight HTTP handlers
//  2. eng.Close()        — only then close the engine
func (e *Engine) Close() {
	close(e.commandCh)
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

// Run starts the sequential processing loop. Call this in a dedicated goroutine.
// It processes one command at a time — this is intentional and is the source
// of our determinism guarantee.
//
// The loop exits when commandCh is closed (via Close()) and fully drained.
// This guarantees every in-flight command is processed before shutdown.
func (e *Engine) Run() {
	log.Printf("[engine] started for symbol %s", e.book.Symbol)

	for cmd := range e.commandCh {
		result := e.process(cmd)
		cmd.ResultCh <- result
	}

	// commandCh is closed and drained — safe to close eventCh now.
	log.Printf("[engine] shutting down")
	close(e.eventCh)
}

// process dispatches a single command. Called only from the engine goroutine.
func (e *Engine) process(cmd orderbook.Command) orderbook.CommandResult {
	switch cmd.Type {

	case orderbook.CmdPlaceOrder:
		return e.processPlace(cmd.Order)

	case orderbook.CmdCancelOrder:
		return e.processCancel(cmd.OrderID)

	default:
		return orderbook.CommandResult{Err: fmt.Errorf("unknown command type: %d", cmd.Type)}
	}
}

func (e *Engine) processPlace(order *orderbook.Order) orderbook.CommandResult {
	if order.ID == 0 {
		order.ID = orderbook.NewID()
	}
	if order.Timestamp == 0 {
		order.Timestamp = time.Now().UnixNano()
	}

	trades, err := e.book.PlaceOrder(order)
	if err != nil {
		return orderbook.CommandResult{Err: err}
	}

	// Broadcast each individual trade as a separate event.
	for _, t := range trades {
		e.emit(TradeEvent{
			Type:      EventTrade,
			Trade:     t,
			Timestamp: t.Timestamp,
		})
	}

	e.emit(TradeEvent{
		Type:      EventBookUpdate,
		Timestamp: time.Now().UnixNano(),
		BookUpdate: &BookUpdateEvent{
			Symbol: e.book.Symbol,
			Bids:   e.book.BidLevels(5),
			Asks:   e.book.AskLevels(5),
		},
	})

	return orderbook.CommandResult{
		Trades:  trades,
		OrderID: order.ID,
	}
}

func (e *Engine) processCancel(orderID uint64) orderbook.CommandResult {
	order, err := e.book.CancelOrder(orderID)
	if err != nil {
		return orderbook.CommandResult{Err: err}
	}

	e.emit(TradeEvent{
		Type:      EventBookUpdate,
		Timestamp: time.Now().UnixNano(),
		BookUpdate: &BookUpdateEvent{
			Symbol: e.book.Symbol,
			Bids:   e.book.BidLevels(5),
			Asks:   e.book.AskLevels(5),
		},
	})

	return orderbook.CommandResult{OrderID: order.ID}
}

// emit sends an event to the broadcast channel. Non-blocking — if the
// broadcaster is slow, events are dropped (market data is best-effort).
func (e *Engine) emit(event TradeEvent) {
	select {
	case e.eventCh <- event:
	default:
		log.Printf("[engine] WARNING: event channel full, dropping %s event", event.Type)
	}
}
