import asyncio
import time
import pytest
from typing import Type, Callable, Any
from unittest.mock import AsyncMock, patch, call


original_asyncio_sleep = asyncio.sleep

from toller.retry import retry_async_call
from toller.exceptions import (
    MaxRetriesExceeded,
    TransientError,
    FatalError,
)


class CustomTransientError(TransientError):
    pass


class CustomFatalError(FatalError):
    pass


class AnotherException(Exception):
    pass


async def succeed_immediately(*args: Any, **kwargs: Any) -> str:
    """A function that always succeeds."""
    return f"Success args={args} kwargs={kwargs}"


def fail_n_times_then_succeed(
    n: int, exception_type: Type[Exception] = CustomTransientError
) -> Callable:
    """Fails n times, then succeeds."""
    call_count = 0

    async def inner() -> str:
        nonlocal call_count
        call_count += 1
        if call_count <= n:
            raise exception_type(f"Failing on call {call_count}")
        return f"Success on call {call_count}"

    return inner


def always_fail(exception_type: Type[Exception] = CustomTransientError) -> Callable:
    """A function that always fails."""

    async def inner() -> str:
        raise exception_type("Always failing")

    return inner


async def test_succeed_on_first_try() -> None:
    """Function succeeds immediately, no retries needed."""
    with patch("toller.retry.asyncio.sleep", new_callable=AsyncMock) as mock_sleep:
        result = await retry_async_call(succeed_immediately, args=(1,), kwargs={"b": 2})
    assert result == "Success args=(1,) kwargs={'b': 2}"
    mock_sleep.assert_not_called()


async def test_fail_once_then_succeed() -> None:
    """Function fails once, then succeeds on the second attempt."""
    failing_func = fail_n_times_then_succeed(1)
    with patch("toller.retry.asyncio.sleep", new_callable=AsyncMock) as mock_sleep:
        result = await retry_async_call(
            failing_func,
            max_attempts=3,
            delay=0.1,
            jitter=False,  # Disable jitter for predictable delay assertion
            retry_on_exception=CustomTransientError,
        )
    assert result == "Success on call 2"
    mock_sleep.assert_called_once_with(pytest.approx(0.1))


async def test_max_attempts_exceeded() -> None:
    """Function fails consistently and exceeds max_attempts."""
    failing_func = always_fail(CustomTransientError)
    start_time = time.monotonic()
    with pytest.raises(MaxRetriesExceeded) as exc_info:
        with patch("toller.retry.asyncio.sleep", new_callable=AsyncMock) as mock_sleep:
            await retry_async_call(
                failing_func,
                max_attempts=4,
                delay=0.05,
                backoff=1.0,  # Fixed delay
                jitter=False,
                retry_on_exception=CustomTransientError,
            )

    end_time = time.monotonic()
    assert exc_info.value.last_exception is not None
    assert isinstance(exc_info.value.last_exception, CustomTransientError)
    assert "failed after 4 attempts" in str(exc_info.value)
    assert mock_sleep.call_count == 3
    mock_sleep.assert_has_calls([call(0.05), call(0.05), call(0.05)])
    assert (end_time - start_time) < 0.5


async def test_fixed_delay_backoff_1() -> None:
    """Test fixed delay using backoff=1.0"""
    failing_func = always_fail(CustomTransientError)
    with pytest.raises(MaxRetriesExceeded):
        with patch("toller.retry.asyncio.sleep", new_callable=AsyncMock) as mock_sleep:
            await retry_async_call(
                failing_func,
                max_attempts=4,
                delay=0.1,
                backoff=1.0,
                jitter=False,
                retry_on_exception=CustomTransientError,
            )
    assert mock_sleep.call_count == 3
    mock_sleep.assert_has_calls([call(0.1), call(0.1), call(0.1)])


async def test_exponential_backoff() -> None:
    """Test exponential backoff."""
    failing_func = always_fail(CustomTransientError)
    with pytest.raises(MaxRetriesExceeded):
        with patch("toller.retry.asyncio.sleep", new_callable=AsyncMock) as mock_sleep:
            await retry_async_call(
                failing_func,
                max_attempts=5,
                delay=0.1,
                backoff=2.0,
                jitter=False,
                retry_on_exception=CustomTransientError,
            )
    assert mock_sleep.call_count == 4
    assert mock_sleep.call_args_list[0] == call(pytest.approx(0.1))
    assert mock_sleep.call_args_list[1] == call(pytest.approx(0.2))
    assert mock_sleep.call_args_list[2] == call(pytest.approx(0.4))
    assert mock_sleep.call_args_list[3] == call(pytest.approx(0.8))


async def test_exponential_backoff_with_max_delay() -> None:
    """Test exponential backoff capped by max_delay."""
    failing_func = always_fail(CustomTransientError)
    with pytest.raises(MaxRetriesExceeded):
        with patch("toller.retry.asyncio.sleep", new_callable=AsyncMock) as mock_sleep:
            await retry_async_call(
                failing_func,
                max_attempts=5,
                delay=0.1,
                backoff=2.0,
                max_delay=0.5,
                jitter=False,
                retry_on_exception=CustomTransientError,
            )
    assert mock_sleep.call_count == 4
    assert mock_sleep.call_args_list[0] == call(pytest.approx(0.1))
    assert mock_sleep.call_args_list[1] == call(pytest.approx(0.2))
    assert mock_sleep.call_args_list[2] == call(pytest.approx(0.4))
    assert mock_sleep.call_args_list[3] == call(pytest.approx(0.5))


async def test_full_jitter() -> None:
    """Test full jitter (sleep time between 0 and calculated delay)."""
    failing_func = always_fail(CustomTransientError)
    delays = []

    async def record_sleep(duration: float) -> None:
        delays.append(duration)
        await original_asyncio_sleep(0)  # Yield control without re-triggering the patch

    with pytest.raises(MaxRetriesExceeded):
        with patch(
            "toller.retry.asyncio.sleep", side_effect=record_sleep
        ) as mock_sleep:
            await retry_async_call(
                failing_func,
                max_attempts=4,
                delay=0.1,
                backoff=2.0,
                jitter=True,
                retry_on_exception=CustomTransientError,
            )
    assert mock_sleep.call_count == 3
    assert len(delays) == 3
    assert 0 <= delays[0] <= 0.1
    assert 0 <= delays[1] <= 0.2
    assert 0 <= delays[2] <= 0.4
    assert sum(delays) > 0


async def test_retry_on_specific_exception_tuple() -> None:
    """Retry only on exceptions specified in a tuple."""
    failing_func = fail_n_times_then_succeed(1, exception_type=CustomTransientError)
    with patch("toller.retry.asyncio.sleep", new_callable=AsyncMock) as mock_sleep:
        result = await retry_async_call(
            failing_func,
            max_attempts=3,
            delay=0.1,
            jitter=False,
            retry_on_exception=(CustomTransientError, AnotherException),
        )
    assert result == "Success on call 2"
    mock_sleep.assert_called_once_with(pytest.approx(0.1))

    failing_func_other = fail_n_times_then_succeed(1, exception_type=ValueError)
    with pytest.raises(ValueError):
        with patch(
            "toller.retry.asyncio.sleep", new_callable=AsyncMock
        ) as mock_sleep_other:
            await retry_async_call(
                failing_func_other,
                max_attempts=3,
                retry_on_exception=(CustomTransientError, AnotherException),
            )
    mock_sleep_other.assert_not_called()


async def test_stop_on_exception_fatal_error() -> None:
    """Stop immediately on FatalError even if it's technically retryable."""
    failing_func = fail_n_times_then_succeed(1, exception_type=CustomFatalError)
    with pytest.raises(CustomFatalError):
        with patch("toller.retry.asyncio.sleep", new_callable=AsyncMock) as mock_sleep:
            await retry_async_call(
                failing_func,
                max_attempts=5,
                retry_on_exception=(CustomFatalError, CustomTransientError),
                stop_on_exception=FatalError,
            )
    mock_sleep.assert_not_called()


async def test_stop_on_exception_custom() -> None:
    """Stop immediately on a custom specified stop_on_exception type."""
    failing_func = fail_n_times_then_succeed(1, exception_type=AnotherException)
    with pytest.raises(AnotherException):
        with patch("toller.retry.asyncio.sleep", new_callable=AsyncMock) as mock_sleep:
            await retry_async_call(
                failing_func,
                max_attempts=5,
                retry_on_exception=Exception,
                stop_on_exception=(AnotherException,),
            )
    mock_sleep.assert_not_called()


async def test_invalid_config() -> None:
    """Test input validation."""
    with pytest.raises(ValueError, match="max_attempts must be greater than 0"):
        await retry_async_call(succeed_immediately, max_attempts=0)
    with pytest.raises(ValueError, match="delay cannot be negative"):
        await retry_async_call(succeed_immediately, delay=-0.1)
    with pytest.raises(ValueError, match="backoff must be >= 1.0"):
        await retry_async_call(succeed_immediately, backoff=0.5)
    with pytest.raises(ValueError, match="max_delay cannot be less than initial delay"):
        await retry_async_call(succeed_immediately, delay=0.5, max_delay=0.1)


async def test_additive_jitter() -> None:
    """Test additive jitter (+/- percentage)."""
    failing_func = always_fail(CustomTransientError)
    delays = []

    async def record_sleep(duration: float) -> None:
        delays.append(duration)
        await original_asyncio_sleep(0)

    with pytest.raises(MaxRetriesExceeded):
        with patch(
            "toller.retry.asyncio.sleep", side_effect=record_sleep
        ) as mock_sleep:
            await retry_async_call(
                failing_func,
                max_attempts=3,  # Fail 1->sleep, Fail 2->sleep, Fail 3->raise
                delay=1.0,  # Base delay
                backoff=1.0,  # Fixed delay base
                jitter=0.1,  # +/- 10%
                retry_on_exception=CustomTransientError,
            )
    assert mock_sleep.call_count == 2
    assert len(delays) == 2
    assert 0.9 <= delays[0] <= 1.1
    assert 0.9 <= delays[1] <= 1.1
    assert delays[0] != 1.0 or delays[1] != 1.0


async def test_multiplicative_jitter() -> None:
    """Test multiplicative jitter (delay * random factor)."""
    failing_func = always_fail(CustomTransientError)
    delays = []

    async def record_sleep(duration: float) -> None:
        delays.append(duration)
        await original_asyncio_sleep(0)

    with pytest.raises(MaxRetriesExceeded):
        with patch(
            "toller.retry.asyncio.sleep", side_effect=record_sleep
        ) as mock_sleep:
            await retry_async_call(
                failing_func,
                max_attempts=3,
                delay=1.0,
                backoff=1.0,
                jitter=(0.5, 1.5),
                retry_on_exception=CustomTransientError,
            )
    assert mock_sleep.call_count == 2
    assert len(delays) == 2
    assert 0.5 <= delays[0] <= 1.5
    assert 0.5 <= delays[1] <= 1.5
    assert delays[0] != 1.0 or delays[1] != 1.0
