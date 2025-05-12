import asyncio
import time
import pytest
import logging
import re

from toller.limiters import CallRateLimiter

logging.getLogger("toller.limiters").setLevel(logging.DEBUG)


async def test_limiter_initialization() -> None:
    """Test the initialization of the CallRateLimiter."""
    limiter = CallRateLimiter(calls_per_second=10, max_burst_calls=100, name="TestInit")
    assert limiter.calls_per_second == 10
    assert limiter.max_burst_calls == 100
    assert limiter.name == "TestInit"
    assert limiter.current_call_allowance == pytest.approx(100, abs=1e-3)

    with pytest.raises(ValueError):
        CallRateLimiter(calls_per_second=0, max_burst_calls=10)
    with pytest.raises(ValueError):
        CallRateLimiter(calls_per_second=10, max_burst_calls=0)


async def test_limiter_acquire_permission_immediate(
    caplog: pytest.LogCaptureFixture,
) -> None:
    """Test immediate permission acquisition without waiting."""
    limiter = CallRateLimiter(calls_per_second=1, max_burst_calls=5)

    start_time = time.monotonic()
    await limiter.acquire_permission(1)
    await limiter.acquire_permission(2)
    end_time = time.monotonic()

    assert (end_time - start_time) < 0.05
    async with limiter._lock:
        await limiter._refill_call_allowance()
        assert limiter._available_calls == pytest.approx(5 - 1 - 2, abs=1e-3)
    assert "Permission acquired for 1.0 call(s)" in caplog.text
    assert "Permission acquired for 2.0 call(s)" in caplog.text


async def test_limiter_acquire_permission_with_wait(
    caplog: pytest.LogCaptureFixture,
) -> None:
    """Test permission acquisition with waiting."""
    limiter = CallRateLimiter(calls_per_second=1, max_burst_calls=1, name="WaitTest")

    await limiter.acquire_permission(1)
    async with limiter._lock:
        assert limiter._available_calls == pytest.approx(0, abs=5e-3)
    caplog.clear()

    start_time = time.monotonic()
    await limiter.acquire_permission(1)
    end_time = time.monotonic()

    assert (end_time - start_time) == pytest.approx(1.0, abs=0.15)
    wait_log_found = any(
        re.search(r"Waiting for approx \d*\.\d{4}s", record.message)
        for record in caplog.records
        if "WaitTest" in record.message and record.name == "toller.limiters"
    )
    assert wait_log_found, "Expected waiting log not found or format mismatch."
    assert "Permission acquired for 1.0 call(s)" in caplog.text
    async with limiter._lock:
        await limiter._refill_call_allowance()
        assert limiter._available_calls == pytest.approx(0, abs=5e-3)


async def test_limiter_burst_capacity(caplog: pytest.LogCaptureFixture) -> None:
    """Test burst capacity of the rate limiter."""
    limiter = CallRateLimiter(calls_per_second=1, max_burst_calls=3, name="BurstTest")

    start_time = time.monotonic()
    await limiter.acquire_permission(1)
    await limiter.acquire_permission(1)
    await limiter.acquire_permission(1)
    end_time = time.monotonic()
    assert (end_time - start_time) < 0.05
    async with limiter._lock:
        assert limiter._available_calls == pytest.approx(0, abs=5e-3)
    caplog.clear()

    start_time = time.monotonic()
    await limiter.acquire_permission(1)
    end_time = time.monotonic()
    assert (end_time - start_time) == pytest.approx(1.0, abs=0.15)
    assert any(
        re.search(r"Waiting for approx", record.message)
        for record in caplog.records
        if "BurstTest" in record.message and record.name == "toller.limiters"
    )
    assert "Permission acquired for 1.0 call(s)" in caplog.text


async def test_limiter_refill_over_time(caplog: pytest.LogCaptureFixture) -> None:
    """Test the refill of call allowance over time."""
    limiter = CallRateLimiter(
        calls_per_second=10, max_burst_calls=10, name="RefillTest"
    )

    await limiter.acquire_permission(10)
    async with limiter._lock:
        assert limiter._available_calls == pytest.approx(0, abs=5e-3)
    caplog.clear()

    await asyncio.sleep(0.5)

    start_time = time.monotonic()
    await limiter.acquire_permission(5)
    end_time = time.monotonic()
    assert (end_time - start_time) < 0.05
    assert "Permission acquired for 5.0 call(s)" in caplog.text
    assert not any(
        re.search(r"Waiting for approx", record.message) for record in caplog.records
    )
    async with limiter._lock:
        assert limiter._available_calls == pytest.approx(0, abs=0.05)


async def test_limiter_cannot_exceed_capacity(caplog: pytest.LogCaptureFixture) -> None:
    """Test that the rate limiter does not allow exceeding its capacity."""
    limiter = CallRateLimiter(
        calls_per_second=1, max_burst_calls=5, name="CapacityCapTest"
    )
    async with limiter._lock:
        limiter._available_calls = 2
        limiter._last_refill_time = time.monotonic() - 100

    caplog.clear()
    await limiter.acquire_permission(1)

    assert "Refilled allowance. Time passed:" in caplog.text
    async with limiter._lock:
        assert limiter._available_calls == pytest.approx(4, abs=1e-3)


async def test_limiter_concurrent_acquires_permission(
    caplog: pytest.LogCaptureFixture,
) -> None:
    """Test concurrent permission acquisition."""
    limiter = CallRateLimiter(
        calls_per_second=2, max_burst_calls=2, name="ConcurrentTest"
    )
    num_tasks = 4
    results = []

    async def worker_task(task_id: int) -> None:
        task_start_time = time.monotonic()
        await limiter.acquire_permission(1)
        task_end_time = time.monotonic()
        duration = task_end_time - task_start_time
        results.append(
            {"id": task_id, "duration": duration, "acquire_time": task_end_time}
        )

    tasks = [asyncio.create_task(worker_task(i)) for i in range(num_tasks)]
    await asyncio.gather(*tasks)
    results.sort(key=lambda r: r["acquire_time"])

    assert results[0]["duration"] < 0.15
    assert results[1]["duration"] < 0.15
    assert (results[2]["acquire_time"] - results[1]["acquire_time"]) == pytest.approx(
        0.5, abs=0.2
    )
    assert (results[3]["acquire_time"] - results[2]["acquire_time"]) == pytest.approx(
        0.5, abs=0.2
    )
    assert (results[3]["acquire_time"] - results[1]["acquire_time"]) == pytest.approx(
        1.0, abs=0.3
    )

    wait_log_found_concurrent = any(
        "ConcurrentTest" in record.message and "Waiting for approx" in record.message
        for record in caplog.records
        if record.name == "toller.limiters"
    )
    assert wait_log_found_concurrent, (
        "Expected 'Waiting for approx' log from ConcurrentTest not found."
    )

    async with limiter._lock:
        await limiter._refill_call_allowance()
        assert limiter._available_calls == pytest.approx(
            0, abs=0.05
        )  # Increased tolerance


async def test_limiter_acquire_permission_value_errors() -> None:
    """Test that acquiring permission raises ValueErrors for invalid inputs."""
    limiter = CallRateLimiter(calls_per_second=1, max_burst_calls=5)
    with pytest.raises(ValueError, match="exceeds maximum burst capacity"):
        await limiter.acquire_permission(6)
    with pytest.raises(ValueError, match="must be positive"):
        await limiter.acquire_permission(0)
    with pytest.raises(ValueError, match="must be positive"):
        await limiter.acquire_permission(-1)


async def test_limiter_as_context_manager(caplog: pytest.LogCaptureFixture) -> None:
    """Test the CallRateLimiter as a context manager."""
    limiter = CallRateLimiter(calls_per_second=1, max_burst_calls=1, name="CtxMgrTest")

    async with limiter:
        assert "Permission acquired for 1.0 call(s)" in caplog.text
        async with limiter._lock:
            assert limiter._available_calls == pytest.approx(0, abs=5e-3)
    caplog.clear()

    start_time = time.monotonic()
    async with limiter:
        pass
    end_time = time.monotonic()
    assert (end_time - start_time) == pytest.approx(1.0, abs=0.15)
    assert any(
        re.search(r"Waiting for approx", record.message)
        for record in caplog.records
        if "CtxMgrTest" in record.message and record.name == "toller.limiters"
    )
    assert "Permission acquired for 1.0 call(s)" in caplog.text


async def test_limiter_fractional_num_calls(caplog: pytest.LogCaptureFixture) -> None:
    """Test the CallRateLimiter with fractional number of calls."""
    limiter = CallRateLimiter(
        calls_per_second=1, max_burst_calls=1.0, name="FractionalTest"
    )

    await limiter.acquire_permission(0.5)
    async with limiter._lock:
        assert limiter._available_calls == pytest.approx(0.5, abs=1e-3)
    assert "Permission acquired for 0.5 call(s)" in caplog.text
    caplog.clear()

    await limiter.acquire_permission(0.5)
    async with limiter._lock:
        assert limiter._available_calls == pytest.approx(0.0, abs=1e-3)
    assert "Permission acquired for 0.5 call(s)" in caplog.text
    caplog.clear()

    start_time = time.monotonic()
    await limiter.acquire_permission(1.0)
    end_time = time.monotonic()
    assert (end_time - start_time) == pytest.approx(1.0, abs=0.15)
    wait_log_found = any(
        re.search(r"Waiting for approx \d*\.\d{4}s", record.message)
        for record in caplog.records
        if "FractionalTest" in record.message and record.name == "toller.limiters"
    )
    assert wait_log_found, "Expected waiting log for ~1s not found or format mismatch."
