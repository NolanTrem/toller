<p align="center">
  <img src="logo.png" alt="Toller Logo" width="400"/>
</p>

## What is Toller?

Toller is a lightweight Python library designed to make your asynchronous calls to microservices, GenAI solutions, external APIs, etc., more robust and reliable. It provides a simple yet powerful decorator to add **rate limiting**, **retries** (with backoff & jitter), and **circuit breaking** to your `async` functions with minimal boilerplate.

Just as the [Nova Scotia Duck Tolling Retriever](https://www.akc.org/dog-breeds/nova-scotia-duck-tolling-retriever/) lures and guides ducks, Toller "lures" unruly asynchronous tasks into well-managed, predictable flows, guiding the overall execution path and making concurrency easier to reason about.

## Why Toller?

Modern applications that integrate with numerous LLMs, vector databases, and other microservices, face a constant challenge: external services can be unreliable. They might be temporarily down, enforce rate limits, or return transient errors.

Building robust applications in this environment means every external call needs careful handling, but repeating this logic for every API call leads to boilerplate, inconsistency, and often, poorly managed asynchronous processes. **Toller was built to solve this.** It provides a declarative way to add these resilience patterns.

Toller offers this standard, both for client-side calls and potentially for protecting server-side resources.

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

## Usage and Examples

<details open>
    <summary>Basic Resilience for Generative AI Calls</summary>
    For a function that calls out to an LLM, we want to handle rate limits, retry on temporary server issues, and stop if the service is truly down.

```python
import asyncio
import random
from toller import TransientError, FatalError, MaxRetriesExceeded, OpenCircuitError

# Define potential API errors
class LLMRateLimitError(TransientError): pass
class LLMServerError(TransientError): pass
class LLMInputError(FatalError): pass # e.g., prompt too long

# Simulate an LLM call
LLM_DOWN_FOR_DEMO = 0 # Counter for demoing circuit breaker
async def call_llm_api(prompt: str):
    global LLM_DOWN_FOR_DEMO
    print(f"LLM API: Processing '{prompt[:20]}...' (Attempt for this task)")
    await asyncio.sleep(random.uniform(0.1, 0.3)) # Network latency

    if LLM_DOWN_FOR_DEMO > 0:
        LLM_DOWN_FOR_DEMO -=1
        print("LLM API: Simulating 503 Service Unavailable")
        raise LLMServerError("LLM service is temporarily down")
    if random.random() < 0.2: # 20% chance of a transient rate limit error
        print("LLM API: Simulating 429 Rate Limit")
        raise LLMRateLimitError("Hit LLM rate limit")
    if len(prompt) < 5:
        print("LLM API: Simulating 400 Bad Request (prompt too short)")
        raise LLMInputError("Prompt is too short")
    
    return f"LLM Response for '{prompt[:20]}...': Generated text."

# Apply Toller
@toller.task(
    # Rate Limiter: 60 calls per minute (1 per sec), burst 5
    rl_calls_per_second=1.0,  # 60 RPM / 60s
    rl_max_burst_calls=5,
    
    # Retries: 3 attempts on transient LLM errors
    retry_max_attempts=3,
    retry_delay=1.0, # Start with 1s delay for LLM errors
    retry_backoff=2.0,
    retry_on_exception=(LLMRateLimitError, LLMServerError),
    retry_stop_on_exception=(LLMInputError,), # Don't retry bad input

    # Circuit Breaker: Opens if retries fail 2 times consecutively
    cb_failure_threshold=2, # Low threshold for demo
    cb_recovery_timeout=20.0, # Wait 20s before one test call
    cb_expected_exception=MaxRetriesExceeded # CB trips when all retries are exhausted
)
async def get_llm_completion(prompt: str):
    return await call_llm_api(prompt)

async def run_example1():
    print("Example 1: Basic Resilience for Generative AI Calls")
    prompts = [
        "Tell me a story about a brave duck.",
        "Explain async programming.",
        "Short", # This will cause a FatalError (LLMInputError)
        "Another valid prompt after fatal.",
        "Prompt to trigger server errors 1", # Will hit retry then MaxRetriesExceeded
        "Prompt to trigger server errors 2", # Will hit retry then MaxRetriesExceeded, tripping CB
        "Prompt after CB should open", # Should hit OpenCircuitError
    ]

    # Simulated LLM service downtime for the relevant prompts
    global LLM_DOWN_FOR_DEMO
    LLM_DOWN_FOR_DEMO = 4

    for i, p in enumerate(prompts):
        print(f"\Sending request: '{p}'")
        try:
            result = await get_llm_completion(p)
            print(f"Success: {result}")
        except MaxRetriesExceeded as e:
            print(f"Toller: Max retries exceeded. Last error: {type(e.last_exception).__name__}: {e.last_exception}")
        except OpenCircuitError as e:
            print(f"Toller: Circuit is open! {e}. Further calls blocked temporarily.")
            if i == len(prompts) - 2: # If this is the call just before the last one
                print("Waiting for circuit breaker recovery timeout for demo...")
                await asyncio.sleep(21) # Wait for CB to go HALF_OPEN
        except FatalError as e:
            print(f"Toller: Fatal error, no retries. Error: {type(e).__name__}: {e}")
        except Exception as e:
            print(f"Toller: Unexpected error. Type: {type(e).__name__}, Error: {e}")
        
        await asyncio.sleep(0.3) # Small pause between top-level requests to see rate limiter too

if __name__ == "__main__":
    asyncio.run(run_example1())
```
</details>


<details open>
    <summary>Shared Rate Limiter for Multiple Related API Calls</summary>
    Often, different API endpoints for the same service share an overall rate limit.

```python
import time
from toller import CallRateLimiter # For creating a shared instance

# Assume these two functions call endpoints that share a single rate limit pool
shared_api_rl = CallRateLimiter(calls_per_second=2, max_burst_calls=2, name="MyServiceSharedRL")

@toller.task(
    rate_limiter_instance=shared_api_rl,
    # Disable retry/CB for this simple RL demo
    enable_retry=False, enable_circuit_breaker=False 
)
async def call_endpoint_a(item_id: int):
    print(f"Calling A for {item_id}...")
    await asyncio.sleep(0.1)
    return f"A {item_id} done"

@toller.task(
    rate_limiter_instance=shared_api_rl,
    enable_retry=False, enable_circuit_breaker=False
)
async def call_endpoint_b(item_id: int):
    print(f"Calling B for {item_id}...")
    await asyncio.sleep(0.1)
    return f"B {item_id} done"

async def run_example2():
    print("\nExample 2: Shared Rate Limiter")
    tasks = []
    # These 4 calls will exceed the burst of 2 for the shared limiter (rate 2/sec), so, some will be delayed.
    tasks.append(call_endpoint_a(1))
    tasks.append(call_endpoint_b(1))
    tasks.append(call_endpoint_a(2))
    tasks.append(call_endpoint_b(2))

    start_time = time.time()
    results = await asyncio.gather(*tasks)
    duration = time.time() - start_time

    for res in results:
        print(f"Shared RL Result: {res}")
    print(f"Total time for 4 calls with shared RL (2/sec, burst 2): {duration:.2f}s (expected > ~1.0s)")

if __name__ == "__main__":
    asyncio.run(run_example2())
```
</details>
