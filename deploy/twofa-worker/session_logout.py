"""Explicit, single-dispatch ChatGPT session logout with credential-free history."""
import asyncio
import hashlib
import hmac
import json
import re
import tempfile
import time
import uuid
from urllib.parse import urlsplit

from fastapi import HTTPException

ACTIVE = frozenset({'queued', 'logging_in', 'revoking'})
CODES = frozenset({'login_failed', 'login_interaction_required', 'invalid_credentials',
                   'account_die', 'logout_control_missing', 'logout_rejected',
                   'logout_unconfirmed', 'worker_interrupted', 'login_access_denied',
                   'login_rate_limited', 'identity_mismatch', 'login_workspace_selection_failed', 'login_session_incomplete'})
LOGOUT_LABEL = re.compile(r'^(?:log\s*out\s*(?:of\s*)?all(?:\s*(?:sessions|devices))?|logout\s*all|sign\s*out\s*(?:of\s*)?all(?:\s*(?:sessions|devices))?|退出所有(?:会话|设备)|登出所有(?:會話|裝置)|đăng\s*xuất\s*tất\s*cả)$', re.I)


class LogoutError(Exception):
    def __init__(self, code):
        self.code = code if code in CODES else 'logout_unconfirmed'
        super().__init__(self.code)


def is_logout_write(url, method):
    """Observe only account logout/revoke writes, never generic session/telemetry."""
    try:
        p = urlsplit(url)
        if p.scheme != 'https' or p.port not in {None, 443} or p.username or p.password:
            return False
    except ValueError:
        return False
    if method.upper() not in {'POST', 'DELETE', 'PUT'}:
        return False
    if not ((p.hostname == 'chatgpt.com' and p.path.startswith('/backend-api/'))
            or (p.hostname == 'auth.openai.com' and p.path.startswith('/api/accounts/'))):
        return False
    segments = p.path.lower().strip('/').split('/')
    return any(re.fullmatch(r'(?:logout|log-out|logout[-_]all|revoke(?:[-_]all)?|revoke[-_]all[-_]sessions|terminate[-_]all)', s) for s in segments)


class LogoutDispatch:
    """The exact native 'all sessions' action may send one matching write."""
    def __init__(self, before_send):
        self.before_send = before_send
        self.armed = False
        self.sent = False
        self.accepted = False
        self.rejected = False
        self.request = None
        self.done = asyncio.Event()

    async def route(self, route):
        request = route.request
        p = urlsplit(request.url)
        if request.method.upper() not in {'GET', 'HEAD', 'OPTIONS'}:
            if not self.armed or self.sent or not is_logout_write(request.url, request.method):
                await route.abort()
                return
            # Durable boundary BEFORE forwarding any possible mutation.
            self.sent = True
            try:
                await self.before_send()
            except BaseException:
                await route.abort()
                self.done.set()
                raise
            self.request = request
            await route.continue_()
            return
        if request.is_navigation_request() and (p.scheme != 'https' or p.hostname not in {'chatgpt.com', 'auth.openai.com'}):
            await route.abort()
            return
        await route.continue_()

    def response(self, response):
        if response.request is not self.request or self.request is None:
            return
        if 200 <= response.status < 300:
            self.accepted = True
        elif response.status >= 400:
            self.rejected = True
        else:
            return
        self.done.set()


async def _visible(locator):
    for i in range(min(await locator.count(), 12)):
        item = locator.nth(i)
        if await item.is_visible():
            return item
    return None


async def revoke_browser_sessions(session, email, before_send, *, browser_factory=None):
    from session_phase import _playwright_password_cookies
    factory = browser_factory
    if factory is None:
        from camoufox.async_api import AsyncCamoufox
        factory = AsyncCamoufox
    cookies = _playwright_password_cookies(session.get('__cookies'))
    if not cookies:
        raise LogoutError('login_failed')
    dispatch = LogoutDispatch(before_send)
    with tempfile.TemporaryDirectory(prefix='session-logout-') as directory:
        async with factory(headless=True, persistent_context=True, user_data_dir=directory,
                           os=['windows'], locale='en-US', geoip=False,
                           viewport={'width': 1440, 'height': 1000}) as context:
            await context.route('**/*', dispatch.route)
            await context.add_cookies(cookies)
            page = context.pages[0] if context.pages else await context.new_page()
            page.on('response', dispatch.response)
            try:
                await page.goto('https://chatgpt.com/#settings/Security', wait_until='domcontentloaded', timeout=45000)
                identity = await page.evaluate("async()=>{const r=await fetch('/api/auth/session',{credentials:'include'});if(!r.ok)return '';const d=await r.json();return d.user?.email||'';}")
                if not isinstance(identity, str) or identity.strip().casefold() != email.strip().casefold():
                    raise LogoutError('identity_mismatch')
                button = None
                for _ in range(30):
                    button = await _visible(page.get_by_role('button', name=LOGOUT_LABEL))
                    if button is not None:
                        break
                    await asyncio.sleep(0.4)
                if button is None:
                    # Newer Account UIs put the action under the active devices row.
                    label = await _visible(page.get_by_text(re.compile(r'^(?:Active sessions?|Logged in devices?|已登录设备|活跃会话)$', re.I)))
                    if label is not None:
                        await label.click(timeout=4000)
                        for _ in range(30):
                            button = await _visible(page.get_by_role('button', name=LOGOUT_LABEL))
                            if button is not None:
                                break
                            await asyncio.sleep(0.4)
                if button is None:
                    raise LogoutError('logout_control_missing')
                original = await button.element_handle()
                dispatch.armed = True
                try:
                    await button.click(timeout=5000)
                except Exception:
                    # A navigation may interrupt click completion after dispatch.
                    if not dispatch.sent:
                        raise LogoutError('logout_control_missing') from None
                for _ in range(20):
                    if dispatch.sent:
                        break
                    dialogs = page.get_by_role('dialog')
                    confirm = None
                    for i in range(min(await dialogs.count(), 5)):
                        candidate = await _visible(dialogs.nth(i).get_by_role('button', name=LOGOUT_LABEL))
                        if candidate is not None and not await candidate.evaluate('(element, original) => element === original', original):
                            confirm = candidate
                            break
                    if confirm is not None:
                        try:
                            await confirm.click(timeout=5000)
                        except Exception:
                            if not dispatch.sent:
                                raise LogoutError('logout_control_missing') from None
                        break
                    await asyncio.sleep(0.25)
                try:
                    await asyncio.wait_for(dispatch.done.wait(), 25)
                except asyncio.TimeoutError:
                    raise LogoutError('logout_unconfirmed') from None
                if dispatch.accepted:
                    return True
                raise LogoutError('logout_rejected' if dispatch.rejected else 'logout_unconfirmed')
            finally:
                page.remove_listener('response', dispatch.response)


class SessionLogoutRuntime:
    def __init__(self, runtime, *, login_fn=None, revoke_fn=None):
        from browser_login import get_browser_session
        self.runtime = runtime
        self.db = runtime.requests
        self.login_fn = login_fn or get_browser_session
        self.revoke_fn = revoke_fn or revoke_browser_sessions
        self.tasks = set()
        self.db.execute('''CREATE TABLE IF NOT EXISTS session_logout_jobs (
            id TEXT PRIMARY KEY, request_id TEXT UNIQUE NOT NULL, digest BLOB NOT NULL,
            email TEXT NOT NULL, status TEXT NOT NULL, error_code TEXT NOT NULL,
            created_at REAL NOT NULL, finished_at REAL)''')
        # No secrets are kept on disk; interrupted work is never resumed.
        self.db.execute("UPDATE session_logout_jobs SET status=CASE WHEN status='revoking' THEN 'needs_review' ELSE 'interrupted' END, error_code='worker_interrupted',finished_at=? WHERE status IN ('queued','logging_in','revoking')", (time.time(),))
        self.db.commit()

    def list_jobs(self):
        rows = self.db.execute('SELECT id,email,status,error_code,created_at,finished_at FROM session_logout_jobs ORDER BY created_at DESC,rowid DESC').fetchall()
        return [dict(zip(('id', 'email', 'status', 'error_code', 'created_at', 'finished_at'), row)) for row in rows]

    def active(self):
        return self.db.execute("SELECT 1 FROM session_logout_jobs WHERE status IN ('queued','logging_in','revoking') LIMIT 1").fetchone() is not None

    def _status(self, job_id, status, error_code=''):
        self.db.execute('UPDATE session_logout_jobs SET status=?,error_code=?,finished_at=? WHERE id=?',
                        (status, error_code if error_code in CODES else '', None if status in ACTIVE else time.time(), job_id))
        self.db.commit()

    async def submit(self, entry):
        if not entry.confirmed:
            raise HTTPException(400, 'confirmation_required')
        encoded = json.dumps(entry.model_dump(), sort_keys=True, ensure_ascii=False).encode()
        digest = hmac.new(self.runtime.digest_key, b'session-logout:' + encoded, hashlib.sha256).digest()
        async with self.runtime.lock:
            prior = self.db.execute('SELECT id,digest FROM session_logout_jobs WHERE request_id=?', (entry.request_id,)).fetchone()
            if prior:
                if not hmac.compare_digest(prior[1], digest):
                    raise HTTPException(409, 'request_id_reused')
                return next(j for j in self.list_jobs() if j['id'] == prior[0])
            if self.active() or any(j.status in {'queued', 'running'} for j in self.runtime.manager.jobs.values()):
                raise HTTPException(409, 'credential_worker_busy')
            if self.db.execute("SELECT 1 FROM session_logout_jobs WHERE email=? AND status='needs_review'", (entry.email,)).fetchone():
                raise HTTPException(409, 'account_has_unresolved_logout')
            if self.db.execute('SELECT COUNT(*) FROM session_logout_jobs').fetchone()[0] >= 1000:
                raise HTTPException(409, 'worker_history_capacity')
            job_id = uuid.uuid4().hex
            self.db.execute('INSERT INTO session_logout_jobs VALUES (?,?,?,?,?,?,?,NULL)',
                            (job_id, entry.request_id, digest, entry.email, 'queued', '', time.time()))
            self.db.commit()
            task = asyncio.create_task(self._run(job_id, entry.model_copy(deep=True)))
            self.tasks.add(task)
            task.add_done_callback(self.tasks.discard)
            return next(j for j in self.list_jobs() if j['id'] == job_id)

    async def _run(self, job_id, entry):
        sent = False
        logged_in = False
        session = None
        async def before_send():
            nonlocal sent
            sent = True
            self._status(job_id, 'revoking')
        try:
            self._status(job_id, 'logging_in')
            session = await asyncio.wait_for(self.login_fn(email=entry.email, password=entry.password,
                                                          secret=entry.mfa_secret), 210)
            if (not isinstance(session, dict) or str(session.get('user', {}).get('email', '')).strip().casefold() != entry.email.casefold()):
                raise LogoutError('identity_mismatch')
            logged_in = True
            result = await asyncio.wait_for(self.revoke_fn(session, entry.email, before_send), 100)
            if result is not True or not sent:
                raise LogoutError('logout_unconfirmed')
            self._status(job_id, 'accepted')
        except asyncio.CancelledError:
            self._status(job_id, 'needs_review' if sent else 'interrupted', 'worker_interrupted')
            raise
        except Exception as exc:
            code = getattr(exc, 'code', '')
            if not logged_in:
                self._status(job_id, 'login_failed', code if code in CODES else 'login_failed')
            else:
                self._status(job_id, 'needs_review' if sent else 'failed', code if code in CODES else 'logout_unconfirmed')
        finally:
            entry.password = ''; entry.mfa_secret = ''
            if isinstance(session, dict):
                session.clear()

    async def shutdown(self):
        tasks = list(self.tasks)
        for task in tasks:
            task.cancel()
        if tasks:
            await asyncio.gather(*tasks, return_exceptions=True)
        self.db.execute("UPDATE session_logout_jobs SET status=CASE WHEN status='revoking' THEN 'needs_review' ELSE 'interrupted' END, error_code='worker_interrupted',finished_at=? WHERE status IN ('queued','logging_in','revoking')", (time.time(),))
        self.db.commit()
