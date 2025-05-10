import asyncio
import functools
import logging
from typing import Callable, Any, Type, Tuple, Coroutine

from .retry import retry_async_call
from .breakers import CircuitBreaker, CircuitState
from .limiters import CallRateLimiter
from .exceptions import (
    TransientError,
    FatalError,
    MaxRetriesExceeded,
    OpenCircuitError,
)

logger = logging.getLogger("toller.decorators")
DecoratedFunc = Callable[..., Coroutine[Any, Any, Any]]


def task(
    *,
    # Retry settings
    enable_retry: bool = True,
    retry_max_attempts: int = 3,
    retry_delay: float = 0.1,
    retry_max_delay: float | None = None,
    retry_backoff: float = 2.0,
    retry_jitter: bool | float | Tuple[float, float] = True,
    retry_on_exception: Type[Exception] | Tuple[Type[Exception], ...] = TransientError,
    retry_stop_on_exception: Type[Exception] | Tuple[Type[Exception], ...] = FatalError,
    # Circuit Breaker settings
    enable_circuit_breaker: bool = True,
    circuit_breaker_instance: CircuitBreaker | None = None,
    cb_failure_threshold: int = 5,
    cb_recovery_timeout: float = 30.0,
    cb_expected_exception: Type[Exception] | Tuple[Type[Exception], ...] = Exception,
    cb_name: str | None = None,
    # Rate Limiter settings
    enable_rate_limiter: bool = True,
    rate_limiter_instance: CallRateLimiter | None = None,
    rl_calls_per_second: float = 10.0,
    rl_max_burst_calls: float = 20.0,
    rl_name: str | None = None,
    rl_consume_calls: float = 1.0,
) -> Callable[[DecoratedFunc], DecoratedFunc]:
    def decorator(func: DecoratedFunc) -> DecoratedFunc:
        _effective_cb: CircuitBreaker | None = None
        _cb_created_internally = False
        _effective_rl: CallRateLimiter | None = None
        _rl_created_internally = False

        if circuit_breaker_instance:
            if enable_circuit_breaker:  # CB only active if enabled
                _effective_cb = circuit_breaker_instance
                logger.debug(
                    f"Using provided CB instance for {func.__name__}: {_effective_cb.name}"
                )
            elif (
                not enable_circuit_breaker and circuit_breaker_instance
            ):  # Log if instance provided but CB disabled
                logger.warning(
                    f"Decorator for '{func.__name__}': A 'circuit_breaker_instance' "
                    f"was provided, but 'enable_circuit_breaker' is False. "
                    f"The provided CB instance will not be used by this decorator."
                )
        elif (
            enable_circuit_breaker
        ):  # Create internal CB if enabled and no instance given
            cb_instance_name = cb_name or f"toller.task.{func.__name__}.cb"
            _effective_cb = CircuitBreaker(
                failure_threshold=cb_failure_threshold,
                recovery_timeout=cb_recovery_timeout,
                expected_exception=cb_expected_exception,
                name=cb_instance_name,
            )
            _cb_created_internally = True
            logger.debug(f"Created new CB for {func.__name__}: {_effective_cb.name}")

        if rate_limiter_instance:
            if enable_rate_limiter:  # RL only active if enabled
                _effective_rl = rate_limiter_instance
                logger.debug(
                    f"Using provided RL instance for {func.__name__}: {_effective_rl.name}"
                )
            elif (
                not enable_rate_limiter and rate_limiter_instance
            ):  # Log if instance provided but RL disabled
                logger.warning(
                    f"Decorator for '{func.__name__}': A 'rate_limiter_instance' "
                    f"was provided, but 'enable_rate_limiter' is False. "
                    f"The provided RL instance will not be used by this decorator."
                )
        elif enable_rate_limiter:  # Create internal RL if enabled and no instance given
            rl_instance_name = rl_name or f"toller.task.{func.__name__}.rl"
            _effective_rl = CallRateLimiter(
                calls_per_second=rl_calls_per_second,
                max_burst_calls=rl_max_burst_calls,
                name=rl_instance_name,
            )
            _rl_created_internally = True
            logger.debug(f"Created new RL for {func.__name__}: {_effective_rl.name}")

        @functools.wraps(func)
        async def wrapper(*args: Any, **kwargs: Any) -> Any:
            async def core_operation() -> Any:
                if enable_retry:
                    logger.debug(f"Executing {func.__name__} with retry logic enabled.")
                    return await retry_async_call(
                        target_func=func,
                        args=args,
                        kwargs=kwargs,
                        max_attempts=retry_max_attempts,
                        delay=retry_delay,
                        max_delay=retry_max_delay,
                        backoff=retry_backoff,
                        jitter=retry_jitter,
                        retry_on_exception=retry_on_exception,
                        stop_on_exception=retry_stop_on_exception,
                    )
                else:
                    logger.debug(
                        f"Executing {func.__name__} directly (retry disabled)."
                    )
                    return await func(*args, **kwargs)

            async def operation_with_cb() -> Any:
                if _effective_cb and enable_circuit_breaker:
                    logger.debug(
                        f"Executing {func.__name__} under CB {_effective_cb.name} "
                        f"(current state: {_effective_cb.state})."
                    )
                    async with _effective_cb:
                        return await core_operation()
                else:
                    active_cb_status = (
                        "not configured"
                        if enable_circuit_breaker
                        else "explicitly disabled"
                    )
                    logger.debug(f"Executing {func.__name__} (CB {active_cb_status}).")
                    return await core_operation()

            if _effective_rl and enable_rate_limiter:
                logger.debug(
                    f"Attempting to acquire permission from RL {_effective_rl.name} for {func.__name__} "
                    f"(consuming {rl_consume_calls} call(s))."
                )
                await _effective_rl.acquire_permission(
                    num_calls_to_make=rl_consume_calls
                )
                logger.debug(
                    f"Permission acquired from RL {_effective_rl.name} for {func.__name__}."
                )
                return await operation_with_cb()
            else:
                active_rl_status = (
                    "not configured" if enable_rate_limiter else "explicitly disabled"
                )
                logger.debug(f"Executing {func.__name__} (RL {active_rl_status}).")
                return await operation_with_cb()

        if _cb_created_internally:
            setattr(wrapper, "_toller_circuit_breaker", _effective_cb)
        if _rl_created_internally:
            setattr(wrapper, "_toller_rate_limiter", _effective_rl)

        return wrapper

    return decorator
