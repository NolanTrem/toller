"""
Exceptions for the Toller library.
"""


class TollerError(Exception):
    """Base class for Toller specific errors."""

    pass


class OpenCircuitError(TollerError):
    """Raised when a call is attempted while the circuit breaker is open."""

    pass


class TransientError(TollerError):
    """An error that indicates a retry might succeed."""

    pass


class FatalError(TollerError):
    """An error that should not be retried."""

    pass


class MaxRetriesExceeded(TollerError):
    """Raised when the maximum number of retries is exhausted."""

    def __init__(self, message, last_exception=None):
        super().__init__(message)
        self.last_exception = last_exception
