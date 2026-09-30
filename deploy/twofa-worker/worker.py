"""Private, authenticated bridge to a pinned Change-2fa-Password-GPT-Auto.

The original desktop HTTP server is never imported or exposed. Its job engine
is reused behind an encrypted repository and a deliberately smaller API.
"""
from __future__ import annotations

import asyncio
import base64
import fcntl
import hashlib
import hmac
import json
import os
import re
import sqlite3
import sys
import uuid
from contextlib import asynccontextmanager
from pathlib import Path
from typing import Any

from cryptography.fernet import Fernet
from fastapi import FastAPI, HTTPException, Request
from fastapi.exceptions import RequestValidationError
from fastapi.responses import JSONResponse
from pydantic import BaseModel, ConfigDict, Field, StrictBool, field_validator

from rotation_service import StagedRotationService, pre_rotation_failure, rotation_failure_code
from session_logout import SessionLogoutRuntime


class RotationInput(BaseModel):
    model_config = ConfigDict(extra="forbid")
    email: str = Field(min_length=3, max_length=254)
    password: str = Field(min_length=1, max_length=4096)
    mfa_secret: str = Field(min_length=16, max_length=256)
    request_id: str
    confirmed: StrictBool = False

    @field_validator("email")
    @classmethod
    def email_valid(cls, value: str) -> str:
        value = value.strip().lower()
        if not re.fullmatch(r"[^\s@]+@[^\s@]+\.[^\s@]+", value):
            raise ValueError("invalid_email")
        return value

    @field_validator("mfa_secret")
    @classmethod
    def secret_valid(cls, value: str) -> str:
        value = value.replace(" ", "").upper().rstrip("=")
        try:
            decoded = base64.b32decode(value + "=" * (-len(value) % 8))
            if len(decoded) < 10:
                raise ValueError("short_secret")
        except Exception:
            raise ValueError("invalid_totp_secret") from None
        return value

    @field_validator("request_id")
    @classmethod
    def id_valid(cls, value: str) -> str:
        return uuid.UUID(value).hex


class EncryptedJobRepository:
    """Keep engine persistence compatible without storing plaintext secrets."""

    fields = ("password", "secret", "account_check", "error")

    def __init__(self, repository: Any, cipher: Fernet):
        self.repository = repository
        self.cipher = cipher

    def _seal(self, row: dict) -> dict:
        result = dict(row)
        for key in self.fields:
            if key in result and result[key] is not None:
                text = result[key] if isinstance(result[key], str) else json.dumps(result[key])
                result[key] = "enc:v1:" + self.cipher.encrypt(text.encode()).decode()
        return result

    def create(self, row: dict):
        return self.repository.create(self._seal(row))

    def update_status(self, job_id: str, status: str, **fields):
        return self.repository.update_status(job_id, status, **self._seal(fields))

    def list_all(self):
        result = []
        for raw in self.repository.list_all():
            row = dict(raw)
            for key in self.fields:
                value = row.get(key)
                if value is not None:
                    if not isinstance(value, str) or not value.startswith("enc:v1:"):
                        raise RuntimeError("Refusing plaintext/foreign worker database")
                    row[key] = self.cipher.decrypt(value[7:].encode()).decode()
            result.append(row)
        return result

    def append_log(self, _job_id: str, _line: str):
        # Engine diagnostics can contain verification codes or response bodies.
        # Do not persist or expose them through this bridge.
        return None

    def get_logs(self, _job_id: str):
        return []

    def recover_interrupted(self):
        for row in self.list_all():
            if row.get("status") not in {"running", "queued", "cancelled"}:
                continue
            state = json.loads(row.get("account_check") or "{}")
            state["error_kind"] = "rotation_interrupted"
            self.update_status(row["id"], "error", error="interrupted", account_check=json.dumps(state))


class Runtime:
    def __init__(self, manager: Any, requests: sqlite3.Connection, digest_key: bytes, lock_file=None):
        self.manager = manager
        self.requests = requests
        self.digest_key = digest_key
        self.lock_file = lock_file  # Holds the exclusive process lock until shutdown.
        self.lock = asyncio.Lock()
        requests.execute("CREATE TABLE IF NOT EXISTS rotation_requests (id TEXT PRIMARY KEY, digest BLOB NOT NULL, job_id TEXT)")
        requests.commit()
        self.session_logout = SessionLogoutRuntime(self)

    def job(self, job_id: str):
        job = self.manager.jobs.get(job_id)
        if job is None or job.mode != "change_2fa":
            raise HTTPException(404, "job_not_found")
        return job

    def summary(self, job: Any) -> dict:
        status = job.status
        failure = pre_rotation_failure(job)
        if failure:
            status = "preflight_failed" if failure == "preflight_failed" else "login_failed"
        if status in {"error", "cancelled"} and not job.rotated_pending_verify and job.error_kind not in {"invalid_credentials", "account_die"}:
            status = "needs_review"
        return {
            "id": job.id, "email": job.email, "status": status,
            "login_verified": job.login_verified,
            "rotated_pending_verify": job.rotated_pending_verify,
            "retryable": job.status == "error" and job.rotated_pending_verify and job.retryable,
            "created_at": job.created_at, "error_code": failure or rotation_failure_code(job),
        }

    async def submit(self, entry: RotationInput) -> dict:
        if not entry.confirmed:
            raise HTTPException(400, "confirmation_required")
        encoded = json.dumps(entry.model_dump(), sort_keys=True, ensure_ascii=False).encode()
        digest = hmac.new(self.digest_key, encoded, hashlib.sha256).digest()
        async with self.lock:
            prior = self.requests.execute("SELECT digest,job_id FROM rotation_requests WHERE id=?", (entry.request_id,)).fetchone()
            if prior:
                if not hmac.compare_digest(prior[0], digest):
                    raise HTTPException(409, "request_id_reused")
                if not prior[1]:
                    raise HTTPException(409, "submission_uncertain_review_jobs")
                return self.summary(self.job(prior[1]))
            if self.session_logout.active():
                raise HTTPException(409, "credential_worker_busy")
            if len(self.manager.jobs) >= 1000:
                raise HTTPException(409, "worker_history_capacity")
            # An earlier partial operation must be resolved before rotating again.
            for job in self.manager.jobs.values():
                if job.email == entry.email and (
                    job.status in {"queued", "running"} or job.rotated_pending_verify
                    or (job.status in {"error", "cancelled"} and job.error_kind not in {"invalid_credentials", "account_die"}
                        and pre_rotation_failure(job) is None)
                ):
                    raise HTTPException(409, "account_has_unresolved_job")
            # Reserve before calling the engine. A crash in the handoff must not
            # cause another irreversible operation on a retried HTTP request.
            self.requests.execute("INSERT INTO rotation_requests VALUES (?,?,NULL)", (entry.request_id, digest))
            self.requests.commit()
            try:
                jobs = self.manager.add([json.dumps({"email": entry.email, "password": entry.password, "mfa_secret": entry.mfa_secret})], "change_2fa")
                job_id = jobs[0]["id"]
                self.requests.execute("UPDATE rotation_requests SET job_id=? WHERE id=?", (job_id, entry.request_id))
                self.requests.commit()
                return self.summary(self.job(job_id))
            except Exception:
                raise HTTPException(503, "submission_uncertain_review_jobs") from None

    async def verify(self, job_id: str) -> dict:
        async with self.lock:
            job = self.job(job_id)
            if not (job.status == "error" and job.rotated_pending_verify and job.retryable):
                raise HTTPException(409, "verification_only_retry_required")
            if self.session_logout.active():
                raise HTTPException(409, "credential_worker_busy")
            self.manager.retry(job_id)
            return self.summary(job)

    def result(self, job_id: str) -> dict:
        job = self.job(job_id)
        if job.status != "success" or not job.login_verified or not job.secret:
            raise HTTPException(409, "result_not_verified")
        latest = next((self.manager.jobs[key] for key in reversed(self.manager.order) if self.manager.jobs[key].email == job.email), None)
        if latest is not job:
            raise HTTPException(409, "result_superseded")
        return {"id": job.id, "email": job.email, "password": job.password, "mfa_secret": job.secret, "login_verified": True}


def build_runtime() -> tuple[Runtime, Any]:
    os.umask(0o077)
    source = Path(os.environ["CHANGE2FA_SOURCE_DIR"]).resolve()
    sys.path.insert(0, str(source))
    # Reuse the MIT-licensed engine, not its unauthenticated desktop bootstrap.
    from db import get_engine, get_repos, get_settings_repo
    from jobs import TwoFAJobManager

    key = os.environ["TWOFA_DATA_KEY"].encode()
    cipher = Fernet(key)
    data = Path(os.environ.get("TWOFA_DATA_DIR", "/data"))
    data.mkdir(parents=True, exist_ok=True, mode=0o700)
    lock_file = (data / "worker.lock").open("a")
    try:
        fcntl.flock(lock_file, fcntl.LOCK_EX | fcntl.LOCK_NB)
    except OSError:
        lock_file.close()
        raise RuntimeError("Only one worker process may use this data directory") from None
    engine = get_engine(str(data / "jobs.db"))
    _, repository, _ = get_repos(engine)
    encrypted = EncryptedJobRepository(repository, cipher)
    encrypted.recover_interrupted()

    class StructuredManager(TwoFAJobManager):
        @staticmethod
        def parse_combo(line):
            entry = json.loads(line)
            # Structured input preserves passwords containing pipes/commas.
            return entry["email"], entry["password"], entry["mfa_secret"]

    manager = StructuredManager(encrypted, get_settings_repo(engine), service=StagedRotationService())
    manager.settings.update({"twofa.max_concurrent": 1, "twofa.auto_retry": False, "twofa.change_enabled": True})
    requests = sqlite3.connect(str(data / "requests.db"), check_same_thread=False)
    return Runtime(manager, requests, hashlib.sha256(key).digest(), lock_file), engine


def create_app(runtime: Runtime | None = None, token: str | None = None) -> FastAPI:
    @asynccontextmanager
    async def lifespan(app):
        engine = None
        actual_token = token if token is not None else os.environ.get("TWOFA_WORKER_TOKEN", "")
        if len(actual_token) < 32:
            raise RuntimeError("TWOFA_WORKER_TOKEN must contain at least 32 characters")
        app.state.token = actual_token
        app.state.runtime = runtime
        if runtime is None:
            app.state.runtime, engine = build_runtime()
            app.state.runtime.manager.start()
        try:
            yield
        finally:
            if app.state.runtime is not None:
                await app.state.runtime.session_logout.shutdown()
            if engine is not None:
                await app.state.runtime.manager.shutdown()
                app.state.runtime.requests.close()
                engine.close()
                app.state.runtime.lock_file.close()

    app = FastAPI(lifespan=lifespan, docs_url=None, redoc_url=None, openapi_url=None)

    @app.middleware("http")
    async def protect(request: Request, call_next):
        if request.url.path != "/health":
            supplied = request.headers.get("authorization", "")
            expected = "Bearer " + request.app.state.token
            if not hmac.compare_digest(supplied.encode(), expected.encode()):
                return JSONResponse({"detail": "unauthorized"}, status_code=401, headers={"Cache-Control": "no-store"})
            length = request.headers.get("content-length", "0")
            if not length.isdigit() or int(length) > 16384:
                return JSONResponse({"detail": "request_too_large"}, status_code=413)
            # Also bound chunked requests; do not trust Content-Length alone.
            body = bytearray()
            async for chunk in request.stream():
                body.extend(chunk)
                if len(body) > 16384:
                    return JSONResponse({"detail": "request_too_large"}, status_code=413)
            request._body = bytes(body)
        response = await call_next(request)
        response.headers["Cache-Control"] = "no-store"
        return response

    @app.exception_handler(RequestValidationError)
    async def invalid(_request, _exception):
        # Pydantic's default error includes the submitted password/secret.
        return JSONResponse({"detail": "invalid_input"}, status_code=422, headers={"Cache-Control": "no-store"})

    @app.exception_handler(Exception)
    async def failed(_request, _exception):
        return JSONResponse({"detail": "worker_error"}, status_code=500, headers={"Cache-Control": "no-store"})

    @app.get("/health")
    async def health():
        return {"ok": True}

    @app.get("/session-logout/jobs")
    async def logout_jobs(request: Request):
        return {"jobs": request.app.state.runtime.session_logout.list_jobs()}

    @app.post("/session-logout/jobs", status_code=202)
    async def start_logout(entry: RotationInput, request: Request):
        return await request.app.state.runtime.session_logout.submit(entry)

    @app.get("/jobs")
    async def jobs(request: Request):
        rt = request.app.state.runtime
        return {"jobs": [rt.summary(rt.manager.jobs[key]) for key in reversed(rt.manager.order)]}

    @app.post("/jobs", status_code=202)
    async def submit(entry: RotationInput, request: Request):
        return await request.app.state.runtime.submit(entry)

    @app.post("/jobs/{job_id}/verify", status_code=202)
    async def verify(job_id: str, request: Request):
        return await request.app.state.runtime.verify(job_id)

    @app.get("/jobs/{job_id}/result")
    async def result(job_id: str, request: Request):
        return request.app.state.runtime.result(job_id)

    return app


app = create_app()
