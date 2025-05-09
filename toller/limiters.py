import asyncio
import time
import logging

logger = logging.getLogger("toller.limiters")


class CallRateLimiter:
    """
    An asynchronous rate limiter for controlling the frequency of calls.

    Uses a token bucket-like mechanism internally, where "calls" are the
    resource being limited. Allows a certain number of calls per unit of time,
    with a configurable burst capacity.
    """

    def __init__(
        self, calls_per_second: float, max_burst_calls: float, name: str | None = None
    ):
        """
        Initializes the CallRateLimiter.

        Args:
            calls_per_second: The rate at which the ability to make calls is replenished (per second).
            max_burst_calls: The maximum number of calls that can be made in a burst.
            name: An optional name for the limiter instance (for logging/monitoring).
        """
        if calls_per_second <= 0:
            raise ValueError("calls_per_second must be positive.")
        if max_burst_calls <= 0:
            raise ValueError("max_burst_calls must be positive.")

        self.calls_per_second = calls_per_second
        self.max_burst_calls = max_burst_calls
        self.name = name or f"CallRateLimiter-{id(self)}"

        self._available_calls: float = max_burst_calls  # Start with full burst capacity
        self._last_refill_time: float = time.monotonic()
        self._lock = asyncio.Lock()  # Protects state transitions

        logger.debug(
            f"Initialized {self.name} with rate={self.calls_per_second}/s, "
            f"burst_capacity={self.max_burst_calls}"
        )

    async def _refill_call_allowance(self) -> None:
        """Replenishes the call allowance based on time elapsed."""
        now = time.monotonic()
        time_passed = now - self._last_refill_time

        if time_passed > 0:
            new_allowance = time_passed * self.calls_per_second
            self._available_calls = min(
                self.max_burst_calls, self._available_calls + new_allowance
            )
            self._last_refill_time = now
            logger.debug(
                f"{self.name}: Refilled allowance. Time passed: {time_passed:.4f}s, "
                f"New allowance (approx): {new_allowance:.4f}, Current allowance: {self._available_calls:.4f}"
            )

    async def acquire_permission(self, num_calls_to_make: float = 1.0) -> None:
        """
        Acquires permission to make a specified number of calls, waiting if necessary.

        Args:
            num_calls_to_make: The number of "calls" this operation represents.
                               Must be <= max_burst_calls.

        Raises:
            ValueError: If `num_calls_to_make` is greater than max_burst_calls or non-positive.
        """
        if num_calls_to_make <= 0:
            raise ValueError("Number of calls to make must be positive.")
        if num_calls_to_make > self.max_burst_calls:
            raise ValueError(
                f"Cannot make {num_calls_to_make} calls, "
                f"as it exceeds maximum burst capacity {self.max_burst_calls}."
            )

        while True:
            async with self._lock:
                await self._refill_call_allowance()  # Refill before checking

                if self._available_calls >= num_calls_to_make:
                    self._available_calls -= num_calls_to_make
                    logger.debug(
                        f"{self.name}: Permission acquired for {num_calls_to_make:.1f} call(s). "
                        f"Remaining allowance: {self._available_calls:.4f}"
                    )
                    return  # Permission acquired

                needed_allowance = num_calls_to_make - self._available_calls
                time_to_wait = max(0, needed_allowance / self.calls_per_second)

            if time_to_wait <= 1e-9:  # If effectively zero, retry lock immediately
                logger.debug(
                    f"{self.name}: Calculated negligible wait time ({time_to_wait:.4f}s), retrying acquire loop."
                )
                await asyncio.sleep(0)
                continue

            logger.debug(
                f"{self.name}: Not enough call allowance ({self._available_calls:.4f} available, "
                f"{num_calls_to_make:.1f} needed). Waiting for approx {time_to_wait:.4f}s."
            )
            await asyncio.sleep(time_to_wait)

    async def __aenter__(self):
        """Acquires permission for one call when entering an async context."""
        await self.acquire_permission(num_calls_to_make=1.0)
        return self

    async def __aexit__(self, exc_type, exc_val, traceback):
        """No action needed on exit."""
        pass

    @property
    def current_call_allowance(self) -> float:
        """
        Returns an estimate of the current call allowance.
        This is an estimate as it doesn't acquire the lock or refill.
        """
        now = time.monotonic()
        time_passed = now - self._last_refill_time
        estimated_refill = time_passed * self.calls_per_second
        return min(self.max_burst_calls, self._available_calls + estimated_refill)
