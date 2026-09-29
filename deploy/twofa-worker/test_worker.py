"""Offline contract tests; never log in to or modify a real account."""
import asyncio
import copy
import importlib
import json
import os
import sqlite3
import sys
import uuid
from pathlib import Path
from types import SimpleNamespace

import pytest
from cryptography.fernet import Fernet
from fastapi import HTTPException
from fastapi.testclient import TestClient

from worker import EncryptedJobRepository, RotationInput, Runtime, create_app

TOKEN = "test-worker-token-" * 3
SECRET = "JBSWY3DPEHPK3PXP"
NEW_SECRET = "KRUGS4ZANFZSAYJA"


def entry(**kwargs):
    return {"email": "test@example.com", "password": "p|a,ss----word", "mfa_secret": SECRET,
            "request_id": uuid.uuid4().hex, "confirmed": True, **kwargs}


class Manager:
    def __init__(self):
        self.jobs, self.order, self.add_calls, self.retry_calls = {}, [], 0, 0

    def add(self, lines, mode):
        self.add_calls += 1
        data = json.loads(lines[0])
        job = SimpleNamespace(id=uuid.uuid4().hex, email=data["email"], password=data["password"],
                              secret=data["mfa_secret"], mode=mode, status="queued", login_verified=False,
                              rotated_pending_verify=False, retryable=True, error_kind=None, created_at=1)
        self.jobs[job.id] = job
        self.order.append(job.id)
        return [{"id": job.id}]

    def retry(self, job_id):
        self.retry_calls += 1
        self.jobs[job_id].status = "queued"


@pytest.fixture
def runtime():
    rt = Runtime(Manager(), sqlite3.connect(":memory:", check_same_thread=False), b"test-key")
    yield rt
    rt.requests.close()


@pytest.fixture
def client(runtime):
    with TestClient(create_app(runtime, TOKEN), raise_server_exceptions=False) as client:
        client.headers["Authorization"] = "Bearer " + TOKEN
        yield client


def test_authenticated_bounded_body_and_no_echo(client):
    assert client.get("/health", headers={"Authorization": ""}).status_code == 200
    assert client.get("/jobs", headers={"Authorization": ""}).status_code == 401
    assert client.get("/api/bootstrap").status_code == 404
    assert client.get("/openapi.json").status_code == 404
    for payload in [entry(confirmed="true"), entry(mfa_secret="sensitive-invalid-seed"), entry(password=""), entry(extra="secret")]:
        result = client.post("/jobs", json=payload)
        assert result.status_code == 422
        assert result.json() == {"detail": "invalid_input"}
        assert result.headers["cache-control"] == "no-store"
    assert client.post("/jobs", json=entry(confirmed=False)).status_code == 400
    assert client.post("/jobs", content=b"x" * 16385).status_code == 413
    assert client.post("/jobs", content=b"x" * 16385, headers={"Content-Length": "1"}).status_code == 413
    assert client.post("/jobs", content=iter([b"x" * 9000, b"y" * 9000])).status_code == 413


def test_submission_retry_preserves_single_job_and_no_secrets(client, runtime):
    data = entry()
    first = client.post("/jobs", json=data)
    assert first.status_code == 202
    assert client.post("/jobs", json=data).json() == first.json()
    assert runtime.manager.add_calls == 1
    job = runtime.manager.jobs[first.json()["id"]]
    assert job.password == data["password"]
    assert client.post("/jobs", json={**data, "password": "changed"}).status_code == 409
    assert client.post("/jobs", json=entry()).status_code == 409
    serialized = client.get("/jobs").text
    assert SECRET not in serialized and data["password"] not in serialized and "log_tail" not in serialized


def test_reserved_request_is_not_resubmitted_after_uncertain_handoff(client, runtime):
    data = entry()
    def fail(*_args):
        raise RuntimeError("secret from engine")
    runtime.manager.add = fail
    assert client.post("/jobs", json=data).status_code == 503
    repeated = client.post("/jobs", json=data)
    assert repeated.status_code == 409
    assert "submission_uncertain" in repeated.text
    assert "secret from engine" not in repeated.text


def test_idempotency_survives_runtime_restart(tmp_path):
    database = tmp_path / "requests.db"
    manager = Manager()
    data = RotationInput(**entry())
    first = Runtime(manager, sqlite3.connect(database), b"key")
    result = asyncio.run(first.submit(data))
    first.requests.close()
    recovered = Runtime(manager, sqlite3.connect(database), b"key")
    assert asyncio.run(recovered.submit(data)) == result
    assert manager.add_calls == 1
    recovered.requests.close()


def test_verify_only_retry_and_latest_verified_result(client, runtime):
    first = client.post("/jobs", json=entry()).json()
    job = runtime.manager.jobs[first["id"]]
    assert client.get(f"/jobs/{job.id}/result").status_code == 409
    job.status = "error"
    assert client.post(f"/jobs/{job.id}/verify").status_code == 409
    assert client.get("/jobs").json()["jobs"][0]["status"] == "needs_review"
    job.rotated_pending_verify, job.secret = True, NEW_SECRET
    assert client.post(f"/jobs/{job.id}/verify").status_code == 202
    assert job.secret == NEW_SECRET and job.rotated_pending_verify
    assert runtime.manager.retry_calls == 1 and runtime.manager.add_calls == 1
    assert client.post(f"/jobs/{job.id}/verify").status_code == 409
    job.status, job.login_verified, job.rotated_pending_verify = "success", True, False
    result = client.get(f"/jobs/{job.id}/result")
    assert result.json()["mfa_secret"] == NEW_SECRET
    assert result.headers["cache-control"] == "no-store"
    assert client.post("/jobs", json=entry(mfa_secret=NEW_SECRET)).status_code == 202
    assert client.get(f"/jobs/{job.id}/result").status_code == 409
    assert client.get("/jobs/missing/result").status_code == 404


class Repository:
    def __init__(self):
        self.rows = {}
    def create(self, row):
        self.rows[row["id"]] = copy.deepcopy(row)
    def update_status(self, job_id, status, **fields):
        self.rows[job_id].update(status=status, **fields)
    def list_all(self):
        return list(self.rows.values())


def test_encryption_includes_errors_and_interrupted_recovery():
    raw = Repository()
    repo = EncryptedJobRepository(raw, Fernet(Fernet.generate_key()))
    repo.create({"id": "one", "status": "running", "password": "private-password", "secret": SECRET,
                 "account_check": json.dumps({"rotated_pending_verify": True}), "error": "private-error"})
    sealed = json.dumps(raw.rows)
    for value in (SECRET, "private-password", "private-error", "rotated_pending_verify"):
        assert value not in sealed
    assert repo.list_all()[0]["secret"] == SECRET
    repo.recover_interrupted()
    recovered = repo.list_all()[0]
    assert recovered["status"] == "error"
    assert json.loads(recovered["account_check"])["rotated_pending_verify"] is True
    assert json.loads(recovered["account_check"])["error_kind"] == "rotation_interrupted"
    raw.rows["one"]["secret"] = SECRET
    with pytest.raises(RuntimeError, match="plaintext"):
        repo.list_all()


@pytest.mark.skipif(not os.getenv("CHANGE2FA_SOURCE_DIR"), reason="pinned engine source not supplied")
def test_pinned_engine_sqlite_checkpoint_verify_and_recovery(tmp_path):
    """Use real upstream jobs and SQLite, a fake service, and no network."""
    sys.path.insert(0, str(Path(os.environ["CHANGE2FA_SOURCE_DIR"]).resolve()))
    from db import get_engine, get_repos, get_settings_repo
    from jobs import TwoFAJobManager
    from service import RotationResult

    engine = get_engine(str(tmp_path / "engine.db"))
    raw = get_repos(engine)[1]
    repo = EncryptedJobRepository(raw, Fernet(Fernet.generate_key()))
    class FakeService:
        rotates, verifies = 0, 0
        async def rotate(self, *, checkpoint, **_kwargs):
            self.rotates += 1
            await checkpoint(NEW_SECRET)
            raise RuntimeError("temporary verification error with private-password")
        async def verify(self, *, new_secret, **_kwargs):
            self.verifies += 1
            return RotationResult(secret=new_secret, login_verified=True)
    fake = FakeService()
    manager = TwoFAJobManager(repo, get_settings_repo(engine), service=fake)
    manager.settings["twofa.auto_retry"] = False
    manager.parse_combo = lambda line: ("test@example.com", "private-password", SECRET)
    manager.add(["structured input"], "change_2fa")
    job = next(iter(manager.jobs.values()))
    asyncio.run(manager._run(job))
    assert job.status == "error" and job.rotated_pending_verify
    assert "private-password" not in json.dumps(raw.list_all())
    rt = Runtime(manager, sqlite3.connect(":memory:"), b"key")
    asyncio.run(rt.verify(job.id))
    asyncio.run(manager._run(job))
    assert fake.rotates == 1 and fake.verifies == 1
    assert rt.result(job.id)["mfa_secret"] == NEW_SECRET
    repo.update_status(job.id, "running")
    repo.recover_interrupted()
    recovered = TwoFAJobManager(repo, get_settings_repo(engine), service=fake)
    assert recovered.jobs[job.id].status == "error"
    assert recovered._queue.empty()
    rt.requests.close()
    engine.close()


def test_proven_login_failure_can_be_resubmitted_only_with_new_confirmation(client, runtime):
    first = client.post('/jobs', json=entry()).json()
    job = runtime.manager.jobs[first['id']]
    job.status = 'error'
    job.error_kind = 'pre_rotation:login_access_denied'
    summary = client.get('/jobs').json()['jobs'][0]
    assert summary['status'] == 'login_failed'
    assert summary['error_code'] == 'login_access_denied'
    assert summary['retryable'] is False
    assert client.post('/jobs/' + job.id + '/verify').status_code == 409
    assert client.post('/jobs', json=entry(confirmed=False)).status_code == 400
    assert runtime.manager.add_calls == 1
    assert client.post('/jobs', json=entry()).status_code == 202
    assert runtime.manager.add_calls == 2
