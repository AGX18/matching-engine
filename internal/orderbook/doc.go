// internal/orderbook/doc.go

// Package orderbook implements the core Limit Order Book (LOB) for a single
// trading pair.
//
// # Data Model
//
// An Order moves through a strict lifecycle from submission to termination:
//
//	New order arrives
//	      │
//	      ▼
//	  StatusOpen ──── full match ──────────────► StatusFilled
//	      │
//	      ├── partial match ──► StatusPartial
//	      │                          │
//	      │                          ├── rest fills ──► StatusFilled
//	      │                          └── cancelled  ──► StatusCancelled
//	      │
//	      └── no match + cancelled ────────────► StatusCancelled
//
// # Price Representation
//
// All prices and quantities are stored as int64 in fixed-point format.
// 1 USD = 1,000,000 units (6 decimal places of precision).
//
//	$1.00      → 1_000_000
//	$29,999.50 → 29_999_500_000
//	$0.000001  → 1  (smallest possible tick)
//
// This avoids floating-point non-determinism. The engine must produce
// identical output for identical input sequences on any machine.
//
// # Price-Time Priority
//
// Matching follows strict Price-Time Priority (FIFO):
//  1. Price: buy orders with higher prices match first.
//     Sell orders with lower prices match first.
//  2. Time: at the same price level, the earliest order matches first.
//
// Trade execution always happens at the RESTING order's price (maker price).
//
// # Concurrency
//
// OrderBook is NOT goroutine-safe by design. All mutations must flow
// through the engine's command channel (see package engine). This gives
// sequential, deterministic processing with zero lock contention.
package orderbook
