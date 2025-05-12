import asyncio
import pytest
import logging
import time
from typing import Type, Callable, Any, Coroutine
from unittest.mock import AsyncMock, patch

from toller.decorators import task
from toller.breakers import CircuitBreaker, CircuitState, OpenCircuitError
from toller.limiters import CallRateLimiter
from toller.exceptions import (
    MaxRetriesExceeded,
    TransientError,
    FatalError,
    TollerError,
)

# Configure loggers for capturing
logging.getLogger("toller.decorators").setLevel(logging.DEBUG)
logging.getLogger("toller.limiters").setLevel(logging.DEBUG)
logging.getLogger("toller.retry").setLevel(logging.DEBUG)


class CustomTransientError(TransientError):
    pass


class CustomFatalError(FatalError):
    pass


class AnotherNonTollerException(Exception):
    pass


async def succeed_immediately(*args: Any, **kwargs: Any) -> str:
    kwargs_str = ", ".join(f"{k}={v}" for k, v in kwargs.items())
    return f"Success args={args} kwargs=[{kwargs_str}]"


def make_fail_n_times_then_succeed(
    n: int,
    exception_type: Type[Exception] = CustomTransientError,
    success_value: str = "Success",
) -> Callable[..., Coroutine[Any, Any, str]]:
    call_count_container = [0]

    async def inner() -> str:
        call_count_container[0] += 1
        if call_count_container[0] <= n:
            raise exception_type(f"Failing on call {call_count_container[0]}")
        return f"{success_value} on call {call_count_container[0]}"

    inner._reset_call_count = lambda: setattr(call_count_container, "0", 0)  # type: ignore
    return inner


def make_always_fail(
    exception_type: Type[Exception] = CustomTransientError,
) -> Callable[..., Coroutine[Any, Any, str]]:
    async def inner() -> str:
        raise exception_type("Always failing")

    return inner


async def test_decorator_success_rl_disabled(caplog: pytest.LogCaptureFixture) -> None:
    @task(enable_rate_limiter=False)
    async def my_successful_task(a: Any, b: Any | None = None) -> str:
        return await succeed_immediately(a, b=b)

    internal_cb_def = getattr(my_successful_task, "_toller_circuit_breaker", None)
    assert internal_cb_def is not None, "Internal CB should be created by default"
    assert any(
        f"Created new CB for my_successful_task: {internal_cb_def.name}" in r.message
        for r in caplog.records
        if r.name == "toller.decorators"
    )
    assert getattr(my_successful_task, "_toller_rate_limiter", None) is None
    caplog.clear()

    with patch(
        "toller.retry.asyncio.sleep", new_callable=AsyncMock
    ) as mock_sleep_retry:
        result = await my_successful_task(1, b=2)
    assert result == "Success args=(1,) kwargs=[b=2]"
    mock_sleep_retry.assert_not_called()
    assert any(
        f"Executing my_successful_task under CB {internal_cb_def.name}" in r.message
        for r in caplog.records
        if r.name == "toller.decorators"
    )
    assert any(
        f"Executing my_successful_task (RL explicitly disabled)" in r.message
        for r in caplog.records
        if r.name == "toller.decorators"
    )


async def test_decorator_cb_opens_on_failures_retry_disabled_rl_disabled(
    caplog: pytest.LogCaptureFixture,
) -> None:
    always_fail_func = make_always_fail(CustomTransientError)

    @task(
        enable_rate_limiter=False,
        enable_retry=False,
        cb_failure_threshold=2,
        cb_recovery_timeout=0.1,
        cb_expected_exception=CustomTransientError,
        cb_name="custom_cb_name_retry_disabled",
    )
    async def my_cb_task() -> str:
        return await always_fail_func()

    internal_cb = getattr(my_cb_task, "_toller_circuit_breaker", None)
    assert internal_cb is not None
    assert any(
        f"Created new CB for my_cb_task: {internal_cb.name}" in r.message
        for r in caplog.records
        if r.name == "toller.decorators"
    )
    caplog.clear()

    with pytest.raises(CustomTransientError):
        await my_cb_task()
    assert internal_cb.current_failures == 1
    decorator_logs_c1 = [
        r.message for r in caplog.records if r.name == "toller.decorators"
    ]
    assert any(
        f"Executing my_cb_task (RL explicitly disabled)" in msg
        for msg in decorator_logs_c1
    )
    assert any(
        f"Executing my_cb_task under CB {internal_cb.name} (current state: CircuitState.CLOSED)"
        in msg
        for msg in decorator_logs_c1
    )
    caplog.clear()

    with pytest.raises(CustomTransientError):
        await my_cb_task()
    assert internal_cb.state == CircuitState.OPEN
    decorator_logs_c2 = [
        r.message for r in caplog.records if r.name == "toller.decorators"
    ]
    assert any(
        f"Executing my_cb_task under CB {internal_cb.name} (current state: CircuitState.CLOSED)"
        in msg
        for msg in decorator_logs_c2
    )
    caplog.clear()

    with pytest.raises(OpenCircuitError):
        await my_cb_task()
    decorator_logs_c3 = [
        r.message for r in caplog.records if r.name == "toller.decorators"
    ]
    assert any(
        f"Executing my_cb_task under CB {internal_cb.name} (current state: CircuitState.OPEN)"
        in msg
        for msg in decorator_logs_c3
    )
    caplog.clear()

    await asyncio.sleep(internal_cb.recovery_timeout + 0.05)
    with pytest.raises(CustomTransientError):
        await my_cb_task()
    assert internal_cb.state == CircuitState.OPEN
    decorator_logs_c4 = [
        r.message for r in caplog.records if r.name == "toller.decorators"
    ]
    assert any(
        f"Executing my_cb_task under CB {internal_cb.name} (current state: CircuitState.OPEN)"
        in msg
        for msg in decorator_logs_c4
    )


async def test_decorator_default_rl_active(caplog: pytest.LogCaptureFixture) -> None:
    @task()
    async def my_default_task() -> str:
        return "success"

    internal_rl = getattr(my_default_task, "_toller_rate_limiter", None)
    assert internal_rl is not None
    assert internal_rl.calls_per_second == 10.0
    assert internal_rl.max_burst_calls == 20.0
    assert any(
        f"Created new RL for my_default_task: {internal_rl.name}" in r.message
        for r in caplog.records
        if r.name == "toller.decorators"
    )

    internal_cb = getattr(my_default_task, "_toller_circuit_breaker", None)
    assert internal_cb is not None
    assert any(
        f"Created new CB for my_default_task: {internal_cb.name}" in r.message
        for r in caplog.records
        if r.name == "toller.decorators"
    )
    caplog.clear()

    await my_default_task()
    decorator_logs = [
        r.message for r in caplog.records if r.name == "toller.decorators"
    ]
    assert any(
        f"Attempting to acquire permission from RL {internal_rl.name}" in msg
        for msg in decorator_logs
    )
    assert any(
        f"Permission acquired from RL {internal_rl.name}" in msg
        for msg in decorator_logs
    )
    assert any(
        f"Executing my_default_task under CB {internal_cb.name}" in msg
        for msg in decorator_logs
    )


async def test_decorator_rl_basic_limiting(caplog: pytest.LogCaptureFixture) -> None:
    @task(
        rl_calls_per_second=1,
        rl_max_burst_calls=1,
        enable_retry=False,
        enable_circuit_breaker=False,
    )
    async def my_limited_task(task_id: int) -> str:
        await asyncio.sleep(0.001)
        return f"Task {task_id} done"

    internal_rl = getattr(my_limited_task, "_toller_rate_limiter")
    assert internal_rl is not None
    assert any(
        f"Created new RL for my_limited_task: {internal_rl.name}" in r.message
        for r in caplog.records
        if r.name == "toller.decorators"
    )
    caplog.clear()

    start_time = time.monotonic()
    await my_limited_task(1)
    await my_limited_task(2)
    duration = time.monotonic() - start_time

    assert duration == pytest.approx(1.0, abs=0.2)

    limiter_logs = [
        r.message
        for r in caplog.records
        if r.name == "toller.limiters" and internal_rl.name in r.message
    ]
    assert any("Waiting for approx" in msg for msg in limiter_logs)


async def test_decorator_rl_consume_calls(caplog: pytest.LogCaptureFixture) -> None:
    @task(
        rl_calls_per_second=5,
        rl_max_burst_calls=5,
        rl_consume_calls=2.0,
        enable_retry=False,
        enable_circuit_breaker=False,
    )
    async def my_consuming_task(task_id: int) -> str:
        return f"Task {task_id} done"

    internal_rl = getattr(my_consuming_task, "_toller_rate_limiter")
    assert internal_rl is not None
    assert any(
        f"Created new RL for my_consuming_task: {internal_rl.name}" in r.message
        for r in caplog.records
        if r.name == "toller.decorators"
    )
    caplog.clear()

    await my_consuming_task(1)
    decorator_logs_call1 = [
        r.message for r in caplog.records if r.name == "toller.decorators"
    ]
    assert any(f"consuming 2.0 call(s)" in msg for msg in decorator_logs_call1)
    async with internal_rl._lock:
        assert internal_rl._available_calls == pytest.approx(5.0 - 2.0, abs=1e-3)
    caplog.clear()

    await my_consuming_task(2)
    async with internal_rl._lock:
        await internal_rl._refill_call_allowance()
        assert internal_rl._available_calls == pytest.approx(3.0 - 2.0, abs=0.1)
    caplog.clear()

    start_time = time.monotonic()
    await my_consuming_task(3)
    duration = time.monotonic() - start_time
    assert duration == pytest.approx(0.2, abs=0.1)


async def test_decorator_rl_disabled_no_effect(
    caplog: pytest.LogCaptureFixture,
) -> None:
    @task(
        enable_rate_limiter=False,
        rl_calls_per_second=0.01,
        rl_max_burst_calls=1,
        enable_retry=False,
        enable_circuit_breaker=False,
    )
    async def my_unlimited_task() -> str:
        return "done"

    assert getattr(my_unlimited_task, "_toller_rate_limiter", None) is None
    assert all(
        "Created new RL for my_unlimited_task" not in r.message
        for r in caplog.records
        if r.name == "toller.decorators"
    )
    caplog.clear()

    start_time = time.monotonic()
    for _ in range(5):
        await my_unlimited_task()
    duration = time.monotonic() - start_time
    assert duration < 0.1

    decorator_logs = [
        r.message for r in caplog.records if r.name == "toller.decorators"
    ]
    # Check for the specific log message indicating RL is disabled
    assert any(
        f"Executing my_unlimited_task (RL explicitly disabled)" in msg
        for msg in decorator_logs
    )
    assert all("Attempting to acquire permission" not in msg for msg in decorator_logs)


async def test_decorator_provided_rl_instance(caplog: pytest.LogCaptureFixture) -> None:
    shared_rl = CallRateLimiter(
        calls_per_second=1, max_burst_calls=1, name="MySharedRL"
    )

    @task(
        rate_limiter_instance=shared_rl,
        enable_retry=False,
        enable_circuit_breaker=False,
    )
    async def task_a() -> str:
        return "A done"

    @task(
        rate_limiter_instance=shared_rl,
        enable_retry=False,
        enable_circuit_breaker=False,
    )
    async def task_b() -> str:
        return "B done"

    assert getattr(task_a, "_toller_rate_limiter", None) is None
    definition_decorator_logs = [
        r.message for r in caplog.records if r.name == "toller.decorators"
    ]
    assert any(
        f"Using provided RL instance for task_a: {shared_rl.name}" in msg
        for msg in definition_decorator_logs
    )
    assert any(
        f"Using provided RL instance for task_b: {shared_rl.name}" in msg
        for msg in definition_decorator_logs
    )
    caplog.clear()

    start_time = time.monotonic()
    await task_a()
    await task_b()
    duration = time.monotonic() - start_time
    assert duration == pytest.approx(1.0, abs=0.2)

    limiter_logs = [
        r.message
        for r in caplog.records
        if r.name == "toller.limiters" and shared_rl.name in r.message
    ]
    assert any("Waiting for approx" in msg for msg in limiter_logs)


async def test_decorator_rl_then_retry_then_cb_opens(
    caplog: pytest.LogCaptureFixture,
) -> None:
    always_fail_transient = make_always_fail(CustomTransientError)

    @task(
        rl_calls_per_second=2,
        rl_max_burst_calls=1,
        rl_name="TriplePlayRL",
        retry_max_attempts=2,
        retry_delay=0.01,
        retry_on_exception=CustomTransientError,
        retry_jitter=False,
        cb_failure_threshold=1,
        cb_recovery_timeout=10,
        cb_expected_exception=MaxRetriesExceeded,
        cb_name="TriplePlayCB",
    )
    async def triple_threat_task(call_num: int) -> str:
        return await always_fail_transient()

    internal_rl = getattr(triple_threat_task, "_toller_rate_limiter")
    internal_cb = getattr(triple_threat_task, "_toller_circuit_breaker")
    assert internal_rl is not None and internal_cb is not None
    assert any(
        f"Created new RL for triple_threat_task: {internal_rl.name}" in r.message
        for r in caplog.records
        if r.name == "toller.decorators"
    )
    assert any(
        f"Created new CB for triple_threat_task: {internal_cb.name}" in r.message
        for r in caplog.records
        if r.name == "toller.decorators"
    )
    caplog.clear()

    start_call1 = time.monotonic()
    with pytest.raises(MaxRetriesExceeded):
        await triple_threat_task(1)
    duration_call1 = time.monotonic() - start_call1
    assert duration_call1 < 0.2
    assert internal_cb.state == CircuitState.OPEN

    decorator_logs_c1 = [
        r.message for r in caplog.records if r.name == "toller.decorators"
    ]
    retry_logs_c1 = [r.message for r in caplog.records if r.name == "toller.retry"]
    limiter_logs_c1 = [
        r.message
        for r in caplog.records
        if r.name == "toller.limiters" and internal_rl.name in r.message
    ]

    assert any(
        f"Attempting to acquire permission from RL {internal_rl.name}" in msg
        for msg in decorator_logs_c1
    )
    assert all("Waiting for approx" not in msg for msg in limiter_logs_c1)
    assert any(
        f"Executing triple_threat_task under CB {internal_cb.name}" in msg
        for msg in decorator_logs_c1
    )
    assert any("Retrying triple_threat_task" in msg for msg in retry_logs_c1)
    caplog.clear()

    start_call2 = time.monotonic()
    with pytest.raises(OpenCircuitError):
        await triple_threat_task(2)
    duration_call2 = time.monotonic() - start_call2
    assert duration_call2 == pytest.approx(0.5, abs=0.2)

    decorator_logs_c2 = [
        r.message for r in caplog.records if r.name == "toller.decorators"
    ]
    retry_logs_c2 = [r.message for r in caplog.records if r.name == "toller.retry"]
    limiter_logs_c2 = [
        r.message
        for r in caplog.records
        if r.name == "toller.limiters" and internal_rl.name in r.message
    ]

    assert any(
        f"Attempting to acquire permission from RL {internal_rl.name}" in msg
        for msg in decorator_logs_c2
    )
    assert any("Waiting for approx" in msg for msg in limiter_logs_c2)
    assert any(
        f"Executing triple_threat_task under CB {internal_cb.name} (current state: CircuitState.OPEN)"
        in msg
        for msg in decorator_logs_c2
    )
    assert all("Retrying triple_threat_task" not in msg for msg in retry_logs_c2)


async def test_decorator_rl_blocks_cb_never_checked_if_rl_slow_enough(
    caplog: pytest.LogCaptureFixture,
) -> None:
    @task(
        rl_calls_per_second=0.1,
        rl_max_burst_calls=1,
        rl_name="SlowRL",
        enable_retry=False,
        cb_failure_threshold=1,
        cb_name="AggroCB",
        cb_expected_exception=AnotherNonTollerException,
    )
    async def very_slow_task() -> str:
        raise AnotherNonTollerException("This will trip CB if reached")

    internal_rl = getattr(very_slow_task, "_toller_rate_limiter")
    internal_cb = getattr(very_slow_task, "_toller_circuit_breaker")
    assert internal_rl is not None and internal_cb is not None
    caplog.clear()

    with pytest.raises(AnotherNonTollerException):
        await very_slow_task()
    assert internal_cb.state == CircuitState.OPEN
    decorator_logs_c1 = [
        r.message for r in caplog.records if r.name == "toller.decorators"
    ]
    limiter_logs_c1 = [
        r.message
        for r in caplog.records
        if r.name == "toller.limiters" and internal_rl.name in r.message
    ]
    assert any(
        f"Attempting to acquire permission from RL {internal_rl.name}" in msg
        for msg in decorator_logs_c1
    )
    assert all("Waiting for approx" not in msg for msg in limiter_logs_c1)
    assert any(
        f"Executing very_slow_task under CB {internal_cb.name} (current state: CircuitState.CLOSED)"
        in msg
        for msg in decorator_logs_c1
    )
    caplog.clear()

    start_time = time.monotonic()
    with pytest.raises(OpenCircuitError):
        await very_slow_task()
    duration = time.monotonic() - start_time
    assert duration == pytest.approx(10.0, abs=1.0)

    decorator_logs_c2 = [
        r.message for r in caplog.records if r.name == "toller.decorators"
    ]
    limiter_logs_c2 = [
        r.message
        for r in caplog.records
        if r.name == "toller.limiters" and internal_rl.name in r.message
    ]
    assert any("Waiting for approx" in msg for msg in limiter_logs_c2)
    assert any(
        f"Executing very_slow_task under CB {internal_cb.name} (current state: CircuitState.OPEN)"
        in msg
        for msg in decorator_logs_c2
    )
