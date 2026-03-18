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
