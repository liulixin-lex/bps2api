"""Explicitly distinguish failures before mutation from uncertain rotations."""
import asyncio
import os
import re

from login_guard import LoginBootstrapError

LOGIN_CODES = frozenset({
    "login_access_denied", "login_rate_limited", "login_bootstrap_rejected",
    "login_interaction_required", "login_state_invalid", "invalid_credentials",
    "login_workspace_selection_failed", "login_session_incomplete",
    "account_die", "login_failed", "preflight_failed",
})
PREFIX = "pre_rotation:"


def rotation_failure_code(job) -> str:
    """Explain an exact pinned-engine failure without relaxing mutation guards."""
    if (job.status != "error" or job.rotated_pending_verify or job.login_verified
            or getattr(job, "password_changed", False)
            or job.error_kind != "technical_error"):
        return ""
    error = str(getattr(job, "error", "") or "")
    match = re.fullmatch(r"Đổi 2FA thất bại: disable old 2FA failed HTTP ([45][0-9]{2})", error)
    if match:
        return "rotation_disable_server_error" if match[1].startswith("5") else "rotation_disable_rejected"
    return ""


def classify_login_error(exc: Exception) -> str:
    current = exc
    seen = set()
    while current is not None and id(current) not in seen:
        seen.add(id(current))
        if isinstance(current, LoginBootstrapError) and current.code in LOGIN_CODES:
            return current.code
        kind = getattr(current, "error_kind", "")
        if kind in {"invalid_credentials", "account_die"}:
            return kind
        current = current.__cause__
    text = str(exc).lower()
    if "invalid_state" in text:
        return "login_state_invalid"
    if "passwordless" in text or "mail_provider" in text:
        return "login_interaction_required"
    return "login_failed"


def pre_rotation_failure(job) -> str | None:
    if (job.status != "error" or job.rotated_pending_verify or job.login_verified
            or getattr(job, "password_changed", False)):
        return None
    kind = str(job.error_kind or "")
    code = kind.removeprefix(PREFIX)
    if kind.startswith(PREFIX) and code in LOGIN_CODES:
        return code
    # Read-only compatibility for the exact pinned engine's original records.
    # Its initial _login failure has this prefix; post-rotation verification
    # retains rotated_pending_verify=True. Interrupted/unknown jobs stay blocked.
    if (kind == "technical_error" and getattr(job, "retry_count", 0) == 0
            and str(getattr(job, "error", "") or "").startswith("Đăng nhập thất bại:")):
        return classify_login_error(RuntimeError(job.error))
    return None


class StagedRotationService:
    def __init__(self, *, login_fn=None, rotate_fn=None, entitlement_fn=None):
        from service import TwoFAService
        default_login, default_rotate = TwoFAService._resolve_dependencies()
        engine = os.environ.get('TWOFA_LOGIN_ENGINE', 'http')
        if engine not in {'http', 'camoufox'}:
            raise ValueError('Unsupported TWOFA_LOGIN_ENGINE')
        if engine == 'camoufox' and login_fn is None:
            from browser_login import get_browser_session
            default_login = get_browser_session
        self.login_fn = login_fn or default_login
        self.rotate_fn = rotate_fn or default_rotate
        self.entitlement_fn = entitlement_fn

    async def rotate(self, **kwargs):
        from service import TwoFAFlowError, TwoFAService
        phase = "login"

        async def login(**inputs):
            nonlocal phase
            session = await self.login_fn(**inputs)
            if phase == "login":
                phase = "preflight"
            return session

        async def rotate(**inputs):
            nonlocal phase
            # Mark BEFORE entering upstream: even its first request may mutate.
            phase = "rotation"
            return await self.rotate_fn(**inputs)

        service = TwoFAService(login_fn=login, rotate_fn=rotate,
                               entitlement_fn=self.entitlement_fn, login_attempts=1)
        try:
            return await service.rotate(**kwargs)
        except asyncio.CancelledError:
            raise
        except Exception as exc:
            if phase != "rotation":
                code = classify_login_error(exc) if phase == "login" else "preflight_failed"
                raise TwoFAFlowError(code, error_kind=PREFIX + code) from None
            raise

    async def verify(self, **kwargs):
        from service import TwoFAService
        service = TwoFAService(login_fn=self.login_fn, rotate_fn=self.rotate_fn,
                               entitlement_fn=self.entitlement_fn, login_attempts=1)
        return await service.verify(**kwargs)
