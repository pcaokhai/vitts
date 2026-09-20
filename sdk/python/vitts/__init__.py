"""Thin client for the ViTTS Vietnamese speech API.

The models in :mod:`vitts.models` are generated from ``docs/api/openapi.yaml``; the
transport below is hand-written, because the parts of this API worth wrapping — chunked
streaming and bounded retries on overload — are the parts a generator cannot produce.
"""

from .client import AsyncVittsClient, Audio, VittsClient, job_status
from .errors import VittsError, VittsTransportError
from .models import (
    ApiKey,
    Job,
    JobCreateRequest,
    JobStatus,
    Problem,
    SynthesisParams,
    SynthesizeRequest,
    UsageReport,
    Voice,
)

__all__ = [
    "ApiKey",
    "AsyncVittsClient",
    "Audio",
    "Job",
    "JobCreateRequest",
    "JobStatus",
    "Problem",
    "SynthesisParams",
    "SynthesizeRequest",
    "UsageReport",
    "VittsClient",
    "VittsError",
    "VittsTransportError",
    "Voice",
    "job_status",
]
