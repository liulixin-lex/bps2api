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

    def delete(self, job_id):
        self.jobs.pop(job_id)
        self.order.remove(job_id)

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


@pytest.mark.parametrize("http_status,code", [(500, "rotation_disable_server_error"),
                                              (502, "rotation_disable_server_error"),
                                              (403, "rotation_disable_rejected")])
def test_disable_error_explanation_does_not_relax_mutation_guards(client, runtime, http_status, code):
    first = client.post('/jobs', json=entry()).json()
    job = runtime.manager.jobs[first['id']]
    job.status = 'error'
    job.error_kind = 'technical_error'
    job.error = f'Đổi 2FA thất bại: disable old 2FA failed HTTP {http_status}'
    original = copy.deepcopy(vars(job))
    response = client.get('/jobs')
    summary = response.json()['jobs'][0]
    assert summary['status'] == 'needs_review'
    assert summary['error_code'] == code and not summary['retryable']
    assert response.headers['cache-control'] == 'no-store'
    assert job.error not in response.text and SECRET not in response.text
    assert client.post(f'/jobs/{job.id}/verify').status_code == 409
    assert client.get(f'/jobs/{job.id}/result').status_code == 409
    assert client.post('/jobs', json=entry()).status_code == 409
    assert vars(job) == original
    assert runtime.manager.add_calls == 1 and runtime.manager.retry_calls == 0


@pytest.mark.parametrize('overrides', [
    {'error': 'Đổi 2FA thất bại: disable old 2FA failed HTTP 500 private-secret'},
    {'error': 'Đổi 2FA thất bại: enroll failed HTTP 500'},
    {'error_kind': 'rotation_interrupted'},
    {'rotated_pending_verify': True},
    {'login_verified': True},
    {'password_changed': True},
    {'status': 'running'},
])
def test_disable_error_classifier_rejects_ambiguous_or_later_stages(overrides):
    from rotation_service import rotation_failure_code
    fields = dict(status='error', error_kind='technical_error', login_verified=False,
                  rotated_pending_verify=False, password_changed=False,
                  error='Đổi 2FA thất bại: disable old 2FA failed HTTP 500')
    assert rotation_failure_code(SimpleNamespace(**{**fields, **overrides})) == ''


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
    assert result.json() == {"id": job.id, "email": job.email, "password": job.password, "mfa_secret": NEW_SECRET, "login_verified": True}
    assert client.get(f"/jobs/{job.id}/result", headers={"Authorization": ""}).status_code == 401
    assert job.password not in client.get("/jobs").text
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
    assert rt.result(job.id)["password"] == "private-password"
    restored = TwoFAJobManager(repo, get_settings_repo(engine), service=fake)
    restored_result = Runtime(restored, sqlite3.connect(":memory:"), b"key")
    assert restored_result.result(job.id) == rt.result(job.id)
    restored_result.requests.close()
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


def finished(client, runtime, **kwargs):
    payload = entry(**kwargs)
    result = client.post('/jobs', json=payload)
    assert result.status_code == 202
    job = runtime.job(result.json()['id'])
    job.status = 'success'
    job.login_verified = True
    return payload, job


def test_delete_history_requires_auth_and_valid_bounded_ids(client):
    assert client.request('DELETE', '/jobs', json={'ids': ['a'*32]}, headers={'Authorization': ''}).status_code == 401
    for ids in [[], ['a'*32]*101, ['../secret'], ['x'*32]]:
        assert client.request('DELETE', '/jobs', json={'ids': ids}).status_code == 422


def test_delete_result_never_replays_request_or_reveals_an_old_secret(client, runtime):
    _, old = finished(client, runtime)
    payload, latest = finished(client, runtime)
    assert client.get('/jobs/' + old.id + '/result').status_code == 409
    result = client.request('DELETE', '/jobs', json={'ids': [latest.id, latest.id]})
    assert result.status_code == 200 and result.json() == {'deleted_ids': [latest.id]}
    assert latest.id not in runtime.manager.jobs
    assert client.get('/jobs/' + latest.id + '/result').status_code == 404
    assert client.get('/jobs/' + old.id + '/result').status_code == 409
    assert client.post('/jobs', json=payload).status_code == 410
    assert runtime.manager.add_calls == 2
    assert client.request('DELETE', '/jobs', json={'ids': [latest.id]}).status_code == 200
    restored = Runtime(runtime.manager, runtime.requests, b'test-key')
    assert not restored.summary(old)['is_latest']
    with pytest.raises(HTTPException) as error:
        restored.result(old.id)
    assert error.value.status_code == 409


@pytest.mark.parametrize('status,pending,error_kind', [('queued',False,None),('running',False,None),('error',True,'technical_error'),('error',False,'technical_error'),('cancelled',False,None)])
def test_delete_protects_whole_selection(client, runtime, status, pending, error_kind):
    _, done = finished(client, runtime, email='finished@example.com')
    _, protected = finished(client, runtime, email='other@example.com')
    protected.status = status
    protected.login_verified = False
    protected.rotated_pending_verify = pending
    protected.error_kind = error_kind
    response = client.request('DELETE', '/jobs', json={'ids': [done.id, protected.id]})
    assert response.status_code == 409
    assert len(runtime.manager.jobs) == 2


def test_pre_mutation_failure_history_is_deletable(client, runtime):
    _, job = finished(client, runtime)
    job.status = 'error'
    job.login_verified = False
    job.error_kind = 'pre_rotation:login_browser_challenge'
    job.error = 'pre_rotation:login_browser_challenge'
    assert runtime.summary(job)['deletable']
    assert client.request('DELETE', '/jobs', json={'ids': [job.id]}).status_code == 200
    assert client.get('/jobs').json()['jobs'] == []
