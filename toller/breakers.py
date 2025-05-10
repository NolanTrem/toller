"""
Circuit breaker patterns for the Toller library.
"""

import asyncio
import time
from enum import Enum, auto
from typing import Type, Tuple

from .exceptions import OpenCircuitError


class CircuitState(Enum):
    CLOSED = auto()
    OPEN = auto()
    HALF_OPEN = auto()


class CircuitBreaker:
    """
    A Circuit Breaker pattern implementation for async functions, preventing
    excessive calls to an already failing service.

    Monitors calls made within its context (`async with breaker:`). If the number
    of failures exceeds a threshold, it opens the circuit, preventing further
    calls for a specified recovery period. After the period, it enters a
    half-open state, allowing one test call. Success closes the circuit,
    failure re-opens it.
    """

    def __init__(
        self,
        failure_threshold: int = 5,
        recovery_timeout: float = 30.0,
        expected_exception: Type[Exception] | Tuple[Type[Exception], ...] = Exception,
        name: str | None = None,
    ):
        """
        Initializes the CircuitBreaker.

        Args:
            failure_threshold: The number of failures required to open the circuit.
            recovery_timeout: Seconds to wait in the OPEN state before allowing
                              a transition to HALF_OPEN.
            expected_exception: The exception type(s) considered as failures.
                                Defaults to any Exception. Can be a specific type
                                or a tuple of types.
            name: An optional name for the breaker instance (useful for logging/monitoring).
        """
        if failure_threshold <= 0:
            raise ValueError("failure_threshold must be greater than 0")
        if recovery_timeout <= 0:
            raise ValueError("recovery_timeout must be greater than 0")

        self.failure_threshold = failure_threshold
        self.recovery_timeout = recovery_timeout
        self.expected_exception = expected_exception
        self.name = name or f"CircuitBreaker-{id(self)}"

        self._state = CircuitState.CLOSED
        self._failure_count = 0
        self._last_failure_time: float | None = None
        self._lock = asyncio.Lock()  # Protects state transitions

        # TODO: Add logger injection

    @property
    def state(self) -> CircuitState:
        """Get the current state of the circuit breaker."""
        return self._state

    @property
    def current_failures(self) -> int:
        """Get the current consecutive failure count."""
        return self._failure_count

    async def __aenter__(self) -> "CircuitBreaker":
        """Checks the breaker state before allowing the call."""
        async with self._lock:
            now = time.monotonic()

            if self._state == CircuitState.OPEN:
                # Check if recovery timeout has passed
                if (
                    self._last_failure_time
                    and (now - self._last_failure_time) >= self.recovery_timeout
                ):
                    # Timeout passed, move to half-open
                    self._state = CircuitState.HALF_OPEN
                    # Reset failure count for the half-open test call
                    self._failure_count = 0
                    # Allow the call in HALF_OPEN state
                else:
                    # Still within recovery timeout, block the call
                    raise OpenCircuitError(f"Circuit breaker '{self.name}' is OPEN")

            # If CLOSED or HALF_OPEN, allow the call attempt
            return self

    async def __aexit__(self, exc_type, exc_val, traceback) -> None:
        """Updates the breaker state based on the call outcome."""
        async with self._lock:
            is_failure = exc_type is not None and issubclass(
                exc_type, self.expected_exception
            )

            if self._state == CircuitState.HALF_OPEN:
                if is_failure:
                    # Failed in half-open state, re-open the circuit immediately
                    self._state = CircuitState.OPEN
                    self._last_failure_time = time.monotonic()
                else:
                    # Succeeded in half-open state, close the circuit
                    self._state = CircuitState.CLOSED
                    self._failure_count = 0
                    self._last_failure_time = None

            elif self._state == CircuitState.CLOSED:
                if is_failure:
                    # Failed in closed state, increment failure count
                    self._failure_count += 1
                    self._last_failure_time = time.monotonic()
                    if self._failure_count >= self.failure_threshold:
                        # Threshold reached, open the circuit
                        self._state = CircuitState.OPEN
                else:
                    # Successful call while closed, reset failure count if it was > 0
                    if self._failure_count > 0:
                        self._failure_count = 0
                        self._last_failure_time = None
