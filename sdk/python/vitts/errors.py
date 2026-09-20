"""Failures the API and the network can hand back."""

from __future__ import annotations

from .models import Problem


class VittsError(Exception):
    """A refusal by the API, carrying the reason it gave.

    The gateway answers RFC 9457 problem documents with a stable ``code`` (ADR-009), so
    branching on ``error.code`` is part of the contract and safe to write against —
    unlike ``str(error)``, which is prose meant for a person.
    """

    def __init__(self, problem: Problem, status: int, retry_after: float | None = None) -> None:
        detail = (problem.detail or "").strip() or problem.title or f"HTTP {status}"
        super().__init__(detail)
        self.code = str(problem.code)
        self.status = status
        self.request_id = problem.request_id
        self.retry_after = retry_after

    @property
    def retryable(self) -> bool:
        """True for the two states the API explicitly tells you to wait out."""
        return self.status in (429, 503)


class VittsTransportError(Exception):
    """A failure to reach the API at all: DNS, TLS, a dropped socket."""
