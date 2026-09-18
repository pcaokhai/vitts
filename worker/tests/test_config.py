"""Config is a trust boundary: a bad environment must stop the process, not a request."""

import pytest

from vitts_worker.config import Config, ConfigError

BASE = {"VITTS_MODEL_REVISION": "c2bfbd6"}
S3 = {
    "VITTS_S3_ENDPOINT": "http://minio:9000",
    "VITTS_S3_BUCKET": "vitts",
    "VITTS_S3_REGION": "us-east-1",
    "VITTS_S3_ACCESS_KEY": "key",
    "VITTS_S3_SECRET_KEY": "secret",
}


def test_defaults_match_documented_values() -> None:
    cfg = Config.from_env(BASE)

    assert cfg.grpc_addr == "[::]:50051"  # grpc.aio needs an explicit host
    assert cfg.model_repo == "zeroweight-ai/ZeroTTS"
    assert cfg.ort_intra_op_threads == 8
    assert cfg.max_text_chars == 3000
    assert cfg.s3 is None
    assert cfg.model_mirror_s3 is None


@pytest.mark.parametrize(
    ("configured", "expected"),
    [
        (":50051", "[::]:50051"),
        ("0.0.0.0:50051", "0.0.0.0:50051"),
        ("127.0.0.1:9999", "127.0.0.1:9999"),
        ("  :50051  ", "[::]:50051"),
    ],
)
def test_listen_address_accepts_go_style_port_only(configured: str, expected: str) -> None:
    assert Config.from_env({**BASE, "VITTS_GRPC_ADDR": configured}).grpc_addr == expected


def test_empty_listen_address_is_rejected() -> None:
    with pytest.raises(ConfigError, match="VITTS_GRPC_ADDR"):
        Config.from_env({**BASE, "VITTS_GRPC_ADDR": "   "})


def test_revision_is_required() -> None:
    with pytest.raises(ConfigError, match="VITTS_MODEL_REVISION"):
        Config.from_env({})


def test_blank_revision_is_rejected() -> None:
    with pytest.raises(ConfigError, match="VITTS_MODEL_REVISION"):
        Config.from_env({"VITTS_MODEL_REVISION": "   "})


def test_partial_s3_configuration_is_rejected() -> None:
    partial = {**BASE, "VITTS_S3_BUCKET": "vitts", "VITTS_S3_ENDPOINT": "http://minio:9000"}

    with pytest.raises(ConfigError, match="VITTS_S3_REGION"):
        Config.from_env(partial)


def test_mirror_without_s3_credentials_is_rejected() -> None:
    with pytest.raises(ConfigError, match="VITTS_MODEL_MIRROR_S3"):
        Config.from_env({**BASE, "VITTS_MODEL_MIRROR_S3": "models"})


def test_mirror_with_full_s3_is_accepted() -> None:
    cfg = Config.from_env({**BASE, **S3, "VITTS_MODEL_MIRROR_S3": "models"})

    assert cfg.model_mirror_s3 == "models"
    assert cfg.s3 is not None
    assert cfg.s3.bucket == "vitts"


@pytest.mark.parametrize("value", ["abc", "0", "-1"])
def test_invalid_thread_count_is_rejected(value: str) -> None:
    with pytest.raises(ConfigError, match="ORT_INTRA_OP_THREADS"):
        Config.from_env({**BASE, "ORT_INTRA_OP_THREADS": value})
