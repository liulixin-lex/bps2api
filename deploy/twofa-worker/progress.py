"""Task-scoped progress: only fixed stage codes may leave the worker."""
from contextlib import contextmanager
from contextvars import ContextVar

LOGIN_PHASES = frozenset({'login_start', 'password', 'mfa', 'mfa_retry', 'workspace', 'session', 'preflight'})
PHASES = LOGIN_PHASES | {'rotating', 'logout_preflight', 'revoking'} | {'verify_' + p for p in LOGIN_PHASES}
_reporter = ContextVar('credential_progress_reporter', default=None)
_verifying = ContextVar('credential_progress_verifying', default=False)


def report_phase(phase):
    phase_reporter()(phase)


def phase_reporter():
    """Capture context for browser callbacks dispatched from another task."""
    reporter = _reporter.get()
    verifying = _verifying.get()
    def report(phase):
        if verifying and phase in LOGIN_PHASES:
            phase = 'verify_' + phase
        if reporter is not None and phase in PHASES:
            reporter(phase)
    return report


@contextmanager
def track_progress(reporter):
    active = True
    def guarded(phase):
        if active:
            reporter(phase)
    token = _reporter.set(guarded)
    try:
        yield
    finally:
        active = False
        _reporter.reset(token)


@contextmanager
def verification_progress(enabled=True):
    token = _verifying.set(enabled)
    try:
        yield
    finally:
        _verifying.reset(token)
