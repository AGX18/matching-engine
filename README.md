# High-Performance Order Matching Engine

A deterministic, low-latency Limit Order Book (LOB) and matching engine written in Go.

This project is designed with a strict focus on hardware mechanical sympathy, zero-allocation hot paths, and lock-free concurrency to guarantee deterministic Price-Time Priority execution.

## 🚀 Architecture & Key Design Decisions

### 1. Lock-Free Concurrency (CSP)

Unlike traditional engines that wrap the order book in `sync.RWMutex`, this engine embraces Go's Communicating Sequential Processes (CSP) model. The `OrderBook` is strictly single-threaded and mutated exclusively by the `Engine` loop via an unbuffered command channel.

* **Result:** Zero lock-contention overhead, eliminating the micro-stutters caused by OS-level thread parking.

### 2. Intrusive Doubly-Linked Lists

Standard linked lists in Go require wrapping data in a `Node{}` struct, requiring two pointer hops and two heap allocations per order.

* **Optimization:** The `Order` struct embeds its own `prev` and `next` pointers. The engine manipulates orders directly within the FIFO queues at $O(1)$ speed.
* **Result:** Perfect cache locality. Orders fit cleanly within two 64-byte L1 CPU cache lines.

### 3. Contiguous Array Price Levels

Instead of Red-Black or B-Trees, price level best-bid/best-ask sorting is maintained via contiguous `[]int64` slices.

* **Optimization:** Because financial exchange price levels are typically tightly clustered (often $< 500$ active ticks), utilizing Go's `copy()` (which translates to SIMD-optimized `memmove`) to shift contiguous arrays in the L1 cache drastically outperforms the main-memory pointer chasing required by binary trees.

### 4. Lock-Free Snowflake ID Generation

String UUIDs destroy database B+Tree index performance and consume 16 bytes.

* **Optimization:** A custom, lock-free implementation of the Twitter Snowflake algorithm using atomic `CompareAndSwap` (CAS) loops.
* **Result:** Generates 8-byte, time-ordered, globally unique `uint64` IDs that require a single CPU clock cycle to compare and provide massive database insert throughput.

### 5. Fixed-Point Arithmetic

Floating-point math (`float64`) introduces hardware-level non-determinism (IEEE 754 rounding errors). All prices and quantities use `int64` fixed-point integers (e.g., $1 USD = 1,000,000) to guarantee perfect state replication for Event Sourcing and crash recovery.

## 📂 Project Structure

```text
├── internal/
│   ├── orderbook/
│   │   ├── order.go       # Core domain structs (Order, Trade, IDs)
│   │   ├── book.go        # Limit Order Book and intrusive linked list
│   │   ├── matching.go    # Price-Time Priority matching logic
│   │   └── book_test.go   # Deterministic state-machine tests
│   └── engine/
│       └── engine.go      # CSP event loop, Command routing, Event emitting
└── README.md
```

## 🛠️ Getting Started

### Prerequisites

* Go 1.21+

### Running the Tests

```bash
go test -v ./internal/orderbook
go test -v -race ./internal/engine
```

## 🔮 Roadmap / Future Work

While the core matching logic is complete, a true production exchange requires wrapping this engine in a highly available, distributed ecosystem.

* [ ] **Write-Ahead Logging (WAL):** Implement a sequencer that appends incoming commands to an NVMe WAL or Kafka topic *before* engine execution, allowing for strict Event Sourcing and crash recovery.
* [ ] **CQRS Architecture:** Wire the `eventCh` to a dedicated PostgreSQL database worker to handle historical order queries, keeping the engine's RAM footprint perfectly flat and focused only on active liquidity.
* [ ] **Network Layer:** Build a fiber/gin HTTP REST API for order ingestion and a WebSocket broadcaster for real-time market data dissemination.
