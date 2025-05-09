import asyncio
import random
import logging
from typing import Callable, Any, Type, Tuple, Coroutine

from .exceptions import MaxRetriesExceeded, FatalError, TransientError

logger = logging.getLogger(__name__)
TargetFunc = Callable[..., Coroutine[Any, Any, Any]]


async def retry_async_call(
    target_func: TargetFunc,
    args: tuple = (),
    kwargs: dict | None = None,
    max_attempts: int = 3,
    delay: float = 0.1,
    max_delay: float | None = None,
    backoff: float = 2.0,
    jitter: bool | float | Tuple[float, float] = True,
    retry_on_exception: Type[Exception] | Tuple[Type[Exception], ...] = TransientError,
    stop_on_exception: Type[Exception] | Tuple[Type[Exception], ...] = FatalError,
    # TODO: Add retry_on_result parameter later
) -> Any:
    """
    Core asynchronous retry logic engine.

    Calls the target_func and retries if specific exceptions occur,
    using configurable delay, backoff, and jitter strategies.

    Args:
        target_func: The asynchronous function to call.
        args: Positional arguments for target_func.
        kwargs: Keyword arguments for target_func.
        max_attempts: Maximum number of attempts (including the first call).
        delay: Initial delay between retries in seconds.
        max_delay: Maximum delay allowed, regardless of backoff calculation.
        backoff: Multiplier for the delay on subsequent retries (e.g., 2 for exponential).
        jitter: Adds randomness to delays.
            - True: Full jitter (random delay between 0 and calculated delay).
            - float (e.g., 0.1): Additive jitter (calculated delay +/- 10%).
            - Tuple[float, float] (e.g., (0.5, 1.5)): Multiplicative jitter
              (calculated delay * random factor between 0.5 and 1.5).
            - False: No jitter.
        retry_on_exception: Exception type or tuple of types to retry on.
                            Defaults to TransientError. Can be set to Exception for broader retries.
        stop_on_exception: Exception type or tuple of types that should immediately
                           stop retries and re-raise, even if they match
                           retry_on_exception. Defaults to FatalError.

    Returns:
        The result of the target_func if successful.

    Raises:
        MaxRetriesExceeded: If max_attempts is reached without success.
        Exception: The last caught exception if it wasn't retryable or
                   if it matched stop_on_exception.
    """
    if kwargs is None:
        kwargs = {}
    if max_attempts <= 0:
        raise ValueError("max_attempts must be greater than 0")
    if delay < 0:
        raise ValueError("delay cannot be negative")
    if backoff < 1.0 and backoff != 0:
        raise ValueError("backoff must be >= 1.0")
    if max_delay is not None and max_delay < delay:
        raise ValueError("max_delay cannot be less than initial delay")

    attempt = 0
    last_exception: Exception | None = None
    current_delay = delay

    while attempt < max_attempts:
        attempt += 1
        try:
            logger.debug(
                f"Attempt {attempt}/{max_attempts} calling {target_func.__name__}..."
            )
            result = await target_func(*args, **kwargs)
            # Successful call
            if attempt > 1:
                logger.info(
                    f"Call to {target_func.__name__} succeeded on attempt {attempt}"
                )
            return result
        except Exception as e:
            last_exception = e
            logger.debug(f"Attempt {attempt} failed with {type(e).__name__}: {e}")

            # Check if the exception forces an immediate stop
            if isinstance(e, stop_on_exception):
                logger.warning(
                    f"Stopping retries for {target_func.__name__} due to non-retryable exception: {type(e).__name__}"
                )
                raise e

            # Check if the exception is designated for retries
            if not isinstance(e, retry_on_exception):
                logger.warning(
                    f"Stopping retries for {target_func.__name__} as exception {type(e).__name__} is not in retryable list."
                )
                raise e

            # Check if this is the last attempt
            if attempt >= max_attempts:
                logger.error(
                    f"Max retries ({max_attempts}) exceeded for {target_func.__name__}."
                )
                break  # Exit loop to raise MaxRetriesExceeded below

            wait_time = current_delay

            # Apply Jitter
            if jitter is True:  # Full Jitter
                wait_time = random.uniform(0, wait_time)
            elif (
                isinstance(jitter, float) and 0 < jitter < 1
            ):  # Additive jitter (+/- percentage)
                variation = wait_time * jitter
                wait_time = random.uniform(wait_time - variation, wait_time + variation)
            elif (
                isinstance(jitter, tuple) and len(jitter) == 2
            ):  # Multiplicative jitter
                min_factor, max_factor = jitter
                if 0 <= min_factor <= max_factor:
                    wait_time *= random.uniform(min_factor, max_factor)
                else:
                    logger.warning(
                        f"Invalid multiplicative jitter tuple: {jitter}. Disabling jitter for this attempt."
                    )

            # Ensure wait time isn't negative
            wait_time = max(0, wait_time)

            logger.info(
                f"Retrying {target_func.__name__} in {wait_time:.2f} seconds "
                f"(attempt {attempt + 1}/{max_attempts}) after error: {type(e).__name__}"
            )
            await asyncio.sleep(wait_time)

            # Update current delay for the next iteration
            if backoff >= 1.0:
                next_delay = current_delay * backoff
                current_delay = (
                    next_delay if max_delay is None else min(next_delay, max_delay)
                )

    # All attempts failed, raise the last exception
    raise MaxRetriesExceeded(
        f"Function '{target_func.__name__}' failed after {max_attempts} attempts.",
        last_exception=last_exception,
    )
