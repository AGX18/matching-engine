# High-Performance Order Matching Engine

A low-latency, in-memory Order Matching Engine built in Go. This system implements a Limit Order Book (LOB) utilizing a strict Price-Time Priority matching algorithm, designed for high-throughput environments like cryptocurrency exchanges or traditional equities trading.

## 🚀 System Architecture & Engineering Focus

* **In-Memory Core:** The entire Limit Order Book state is maintained in RAM using highly optimized data structures (Hash Maps and Doubly Linked Lists) to guarantee $O(1)$ order cancellation and $O(1)$ time-priority insertion.
* **Concurrency:** Leverages Go's CSP (Communicating Sequential Processes) concurrency model. Incoming concurrent API requests are funneled through buffered channels into a deterministic, single-threaded matching loop to eliminate race conditions without relying on slow mutex locks.
* **Separation of Concerns:** The core matching domain is strictly isolated from the networking/transport layer, ensuring the financial logic is 100% testable without mocking network I/O.
* **Real-time Event Broadcasting:** Emits trade execution events and order book state updates via WebSockets for downstream consumers.

## 🛠️ Tech Stack

* **Language:** Go (Golang)
* **Transport:** REST (Order Ingestion) & WebSockets (Market Data feed)
* **Deployment:** Docker & Docker Compose
* **Testing:** Standard Go testing library with high coverage on the critical matching path.

## 📂 Project Structure

```text
├── cmd/
│   └── engine/
│       └── main.go             # Application entry point
├── internal/
│   ├── orderbook/              # Core domain: strict memory and financial logic
│   │   ├── order.go            # Data structures for Orders and Trades
│   │   ├── orderbook.go        # LOB implementation and matching algorithm
│   │   └── orderbook_test.go   # Unit tests for the matching engine
│   ├── api/                    # Networking layer (REST / WebSockets)
├── Dockerfile                  # Containerization instructions
├── docker-compose.yml          # Local environment orchestration
└── Makefile                    # Build and test automation
```

## ⚙️ Getting Started

### Prerequisites

* Go 1.21+
* Docker & Docker Compose
* Make

### Running Locally

1. **Clone the repository:**

   ```bash
   git clone https://github.com/agx18/matching-engine.git
   cd matching-engine
   ```

2. **Run via Docker Compose:**

   ```bash
   docker-compose up --build
   ```

   The API will be available at `http://localhost:8080`.

3. **Run tests:**

   ```bash
   make test
   ```
