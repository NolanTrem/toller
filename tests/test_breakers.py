import asyncio
import pytest

from toller import CircuitBreaker, CircuitState, OpenCircuitError

pytestmark = pytest.mark.asyncio


class MockException(Exception):
    """Custom exception for testing."""

    pass


class AnotherMockException(Exception):
    """Another custom exception for testing."""

    pass


async def mock_successful_call():
    """Simulates a call that succeeds."""
    await asyncio.sleep(0.01)
    return "Success"


async def mock_failing_call(exception_type=MockException):
    """Simulates a call that fails."""
    await asyncio.sleep(0.01)
    raise exception_type("Operation failed")


async def test_initial_state_is_closed():
    """Test that the initial state of the circuit breaker is CLOSED."""
    breaker = CircuitBreaker()
    assert breaker.state == CircuitState.CLOSED
    assert breaker.current_failures == 0


async def test_successful_calls_keep_closed():
    """Test that successful calls keep the circuit breaker in CLOSED state."""
    breaker = CircuitBreaker()
    for _ in range(5):
        async with breaker:
            await mock_successful_call()
    assert breaker.state == CircuitState.CLOSED
    assert breaker.current_failures == 0


async def test_failures_increment_count():
    """Failures should increment the failure count but not open the circuit if below threshold."""
    breaker = CircuitBreaker(failure_threshold=5)
    for i in range(3):
        with pytest.raises(MockException):
            async with breaker:
                await mock_failing_call()
        assert breaker.current_failures == i + 1
    assert breaker.state == CircuitState.CLOSED


async def test_failure_threshold_opens_circuit():
    """Test that reaching the failure threshold opens the circuit."""
    threshold = 3
    breaker = CircuitBreaker(failure_threshold=threshold)
    for _ in range(threshold):
        with pytest.raises(MockException):
            async with breaker:
                await mock_failing_call()
    assert breaker.state == CircuitState.OPEN
    assert breaker.current_failures == threshold


async def test_open_circuit_blocks_calls():
    """Test that an open circuit blocks calls immediately."""
    breaker = CircuitBreaker(failure_threshold=1)
    with pytest.raises(MockException):
        async with breaker:
            await mock_failing_call()
    assert breaker.state == CircuitState.OPEN

    with pytest.raises(OpenCircuitError):
        async with breaker:
            await mock_failing_call()


async def test_recovery_timeout_moves_to_half_open():
    """Test that after the recovery timeout, the circuit moves to HALF_OPEN state."""
    recovery_time = 0.1
    breaker = CircuitBreaker(failure_threshold=1, recovery_timeout=recovery_time)

    with pytest.raises(MockException):
        async with breaker:
            await mock_failing_call()
    assert breaker.state == CircuitState.OPEN

    await asyncio.sleep(recovery_time * 0.8)
    with pytest.raises(OpenCircuitError):
        async with breaker:
            await mock_failing_call()

    await asyncio.sleep(recovery_time * 0.3)

    with pytest.raises(MockException):
        async with breaker:
            await mock_failing_call()
    assert breaker.state == CircuitState.OPEN


async def test_success_in_half_open_closes_circuit():
    """Test that a successful call in HALF_OPEN state closes the circuit."""
    recovery_time = 0.1
    breaker = CircuitBreaker(failure_threshold=1, recovery_timeout=recovery_time)

    with pytest.raises(MockException):
        async with breaker:
            await mock_failing_call()
    assert breaker.state == CircuitState.OPEN

    await asyncio.sleep(recovery_time + 0.05)

    async with breaker:
        result = await mock_successful_call()
    assert result == "Success"
    assert breaker.state == CircuitState.CLOSED
    assert breaker.current_failures == 0


async def test_failure_in_half_open_reopens_circuit():
    """Test that a failure in HALF_OPEN state reopens the circuit."""
    recovery_time = 0.1
    breaker = CircuitBreaker(failure_threshold=1, recovery_timeout=recovery_time)

    with pytest.raises(MockException):
        async with breaker:
            await mock_failing_call()
    assert breaker.state == CircuitState.OPEN

    await asyncio.sleep(recovery_time + 0.05)

    with pytest.raises(MockException):
        async with breaker:
            await mock_failing_call()

    assert breaker.state == CircuitState.OPEN

    with pytest.raises(OpenCircuitError):
        async with breaker:
            await mock_failing_call()


async def test_success_resets_failure_count_when_closed():
    """Test that a successful call resets the failure count when in CLOSED state."""
    breaker = CircuitBreaker(failure_threshold=3)

    with pytest.raises(MockException):
        async with breaker:
            await mock_failing_call()
    with pytest.raises(MockException):
        async with breaker:
            await mock_failing_call()
    assert breaker.current_failures == 2
    assert breaker.state == CircuitState.CLOSED

    async with breaker:
        await mock_successful_call()
    assert breaker.current_failures == 0
    assert breaker.state == CircuitState.CLOSED

    with pytest.raises(MockException):
        async with breaker:
            await mock_failing_call()
    assert breaker.current_failures == 1


async def test_expected_exception_filtering():
    """Test that only expected exceptions trip the circuit breaker."""
    breaker = CircuitBreaker(failure_threshold=2, expected_exception=MockException)

    with pytest.raises(AnotherMockException):
        async with breaker:
            await mock_failing_call(exception_type=AnotherMockException)
    assert breaker.current_failures == 0
    assert breaker.state == CircuitState.CLOSED

    with pytest.raises(MockException):
        async with breaker:
            await mock_failing_call(exception_type=MockException)
    assert breaker.current_failures == 1
    with pytest.raises(MockException):
        async with breaker:
            await mock_failing_call(exception_type=MockException)
    assert breaker.current_failures == 2

    assert breaker.state == CircuitState.OPEN


async def test_expected_exception_tuple():
    """Test that a tuple of exceptions can be used to trip the circuit breaker."""
    breaker = CircuitBreaker(
        failure_threshold=2, expected_exception=(MockException, AnotherMockException)
    )

    with pytest.raises(AnotherMockException):
        async with breaker:
            await mock_failing_call(exception_type=AnotherMockException)
    assert breaker.current_failures == 1

    with pytest.raises(MockException):
        async with breaker:
            await mock_failing_call(exception_type=MockException)
    assert breaker.current_failures == 2
    assert breaker.state == CircuitState.OPEN


async def test_concurrent_failures_open_circuit():
    """Test that concurrent failures can open the circuit."""
    threshold = 5
    breaker = CircuitBreaker(failure_threshold=threshold)
    num_concurrent = 10

    async def concurrent_task():
        try:
            async with breaker:
                await mock_failing_call()
        except (MockException, OpenCircuitError):
            pass

    tasks = [asyncio.create_task(concurrent_task()) for _ in range(num_concurrent)]
    await asyncio.gather(*tasks)

    assert breaker.state == CircuitState.OPEN
    assert breaker.current_failures >= threshold


async def test_invalid_init_args():
    with pytest.raises(ValueError):
        CircuitBreaker(failure_threshold=0)
    with pytest.raises(ValueError):
        CircuitBreaker(recovery_timeout=0)
    with pytest.raises(ValueError):
        CircuitBreaker(recovery_timeout=-10)
