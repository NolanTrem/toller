<p align="center">
  <img src="logo.png" alt="Toller Logo" width="400"/>
</p>

## What is Toller?

Toller is a lightweight Python library designed to make your asynchronous calls to microservices, GenAI solutions, external APIs, etc., more robust and reliable. It provides a simple yet powerful decorator to add **rate limiting**, **retries** (with backoff & jitter), and **circuit breaking** to your `async` functions with minimal boilerplate.

Just as the [Nova Scotia Duck Tolling Retriever](https://www.akc.org/dog-breeds/nova-scotia-duck-tolling-retriever/) lures and guides ducks, Toller "lures" unruly asynchronous tasks into well-managed, predictable flows, guiding the overall execution path and making concurrency easier to reason about.

When deploying applications that rely on numerous external async calls, managing failures (downtime, rate limits, transient errors) in a standard way becomes critical. Toller offers this standard, both for client-side calls and potentially for protecting server-side resources.

## Features

*   **`@toller.task` Decorator:** A single, easy-to-use decorator to apply all resilience patterns.
*   **Rate Limiting:**
    *   Async-safe Token Bucket-based `CallRateLimiter`.
    *   Configurable call rates and burst capacity.
    *   Automatic asynchronous waiting when limits are hit.
*   **Retries:**
    *   Strategies: Max attempts, fixed delay, exponential backoff with jitter.
    *   Conditional retries on specific exceptions (e.g., `TransientError`).
    *   Conditional stopping on specific exceptions (e.g., `FatalError`).
    *   Raises `MaxRetriesExceeded` wrapping the last encountered error.
*   **Circuit Breaker:**
    *   Standard states: CLOSED, OPEN, HALF_OPEN.
    *   Configurable failure thresholds and recovery timeouts.
    *   Trips on specified exceptions (e.g., `MaxRetriesExceeded`, or custom fatal errors).
    *   Prevents calls to a failing service, allowing it time to recover.
*   **Custom Exception Hierarchy:** Clear exceptions like `OpenCircuitError`, `TransientError`, `FatalError` for better error handling.
*   **Async Native:** Built for `asyncio`.
*   **Lightweight:** Minimal dependencies.

## Installation

```bash
pip install toller
```
