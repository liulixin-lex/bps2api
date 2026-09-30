"""Unattended login using the pinned upstream browser flow.

Each login gets a disposable profile, including post-rotation verification.
This module never changes MFA settings or exposes a browser control port.
"""
import asyncio
from contextvars import ContextVar
from pathlib import Path
import re
import tempfile
from types import SimpleNamespace
from urllib.parse import urlsplit

from login_guard import LoginBootstrapError, validate_authorize_url, validate_oauth_response
from progress import report_phase, phase_reporter

_settings = ContextVar('twofa_browser_settings')
_login_failure = ContextVar('twofa_login_failure', default=None)


def record_login_response(response, state):
    """Record fixed phase/status codes only; never inspect tokens or response bodies."""
    if state is None:
        return
    parsed = urlsplit(response.url)
    phases = {'/api/accounts/password/verify': 'login_password_rejected',
              '/api/accounts/mfa/verify': 'login_mfa_rejected',
              '/api/accounts/workspace/select': 'login_workspace_selection_failed'}
    if parsed.scheme != 'https' or parsed.hostname != 'auth.openai.com' or parsed.path not in phases or response.request.method != 'POST':
        return
    if parsed.path == '/api/accounts/mfa/verify':
        state['mfa_response'] = response
        if state.get('mfa_event') is not None:
            state['mfa_event'].set()
    if state.get('code'):
        return
    if str(response.headers.get('cf-mitigated', '')).strip().lower() == 'challenge':
        state['code'] = 'login_browser_challenge'
    elif response.status == 429:
        state['code'] = 'login_rate_limited'
    elif 500 <= response.status <= 599:
        state['code'] = 'login_upstream_error'
    elif response.status in {400, 401, 403}:
        state['code'] = phases[parsed.path]


def raise_login_failure():
    state = _login_failure.get()
    if state and state.get('code'):
        raise LoginBootstrapError(state['code'])


def browser_failure_code(exc: BaseException) -> str:
    """Preserve typed guard errors wrapped by the pinned browser engine.

    The engine raises SessionError from LoginBootstrapError. Never parse a
    code out of arbitrary response text or expose the wrapper's diagnostics.
    """
    current = exc
    seen = set()
    allowed = {'login_bootstrap_rejected', 'login_interaction_required',
               'login_access_denied', 'login_rate_limited', 'login_failed',
               'login_browser_challenge', 'login_email_verification_required',
               'login_password_rejected', 'login_mfa_rejected', 'login_mfa_retry_exhausted', 'login_upstream_error',
               'invalid_credentials', 'account_die', 'login_workspace_selection_failed',
               'login_session_incomplete'}
    while current is not None and id(current) not in seen:
        seen.add(id(current))
        if isinstance(current, LoginBootstrapError) and current.code in allowed:
            return current.code
        current = current.__cause__ or current.__context__
    text = str(exc).lower()
    if 'email verification' in text:
        return 'login_email_verification_required'
    if 'totp input not found' in text:
        return 'login_interaction_required'
    if 'password error' in text:
        return 'invalid_credentials'
    if 'account deactivated' in text:
        return 'account_die'
    if 'timeout waiting session cookies' in text:
        return 'login_session_incomplete'
    return 'login_failed'


def is_workspace_page(url: str) -> bool:
    try:
        parsed = urlsplit(url)
        return (parsed.scheme == 'https' and parsed.hostname == 'auth.openai.com'
                and parsed.port in {None, 443} and parsed.username is None
                and parsed.password is None and parsed.path.rstrip('/') == '/workspace')
    except ValueError:
        return False


async def select_personal_workspace(page):
    """Finish the observed post-MFA workspace step using its native control.

    Only the explicitly labeled personal account is selected. Never choose
    an arbitrary organization or replay a submitted choice after navigation.
    """
    if not is_workspace_page(page.url):
        raise LoginBootstrapError('login_workspace_selection_failed')
    report_phase('workspace')
    label = re.compile(r'\bPersonal account\b|个人账户|个人账号|個人帳戶', re.I)
    for _ in range(20):
        candidates = [page.get_by_role('button', name=label),
                      page.get_by_role('link', name=label),
                      page.get_by_text(re.compile(r'^(?:Personal account|个人账户|个人账号|個人帳戶)$', re.I)).locator('xpath=ancestor-or-self::*[self::button or @role="button" or self::a[@href]][1]')]
        for locator in candidates:
            count = await locator.count()
            if count > 20:
                raise LoginBootstrapError('login_workspace_selection_failed')
            visible = []
            for index in range(count):
                item = locator.nth(index)
                if await item.is_visible():
                    visible.append(item)
            if len(visible) > 1:
                raise LoginBootstrapError('login_workspace_selection_failed')
            if not visible:
                continue
            item = visible[0]
            href = await item.get_attribute('href')
            if href:
                from urllib.parse import urljoin
                target = urlsplit(urljoin(page.url, href))
                if (target.scheme != 'https' or target.hostname != 'auth.openai.com'
                        or target.port not in {None, 443} or target.username or target.password):
                    raise LoginBootstrapError('login_workspace_selection_failed')
            if not is_workspace_page(page.url):
                raise LoginBootstrapError('login_workspace_selection_failed')
            try:
                await item.click(timeout=5000)
            except Exception:
                # Navigation can destroy the click context. Never click again;
                # the existing cookie wait decides whether login completed.
                pass
            return
        await asyncio.sleep(0.25)
    raise LoginBootstrapError('login_workspace_selection_failed')


async def fresh_totp(secret, previous_code=None):
    # Generate only after the field is ready, with enough time to fill/submit.
    from totp_helper import generate_code, time_remaining
    remaining = time_remaining()
    if remaining <= 5:
        await asyncio.sleep(remaining + 0.2)
    code = generate_code(secret)
    if code == previous_code:
        await asyncio.sleep(time_remaining() + 0.2)
        code = generate_code(secret)
    return code


def totp_error_is_retryable(data):
    if not isinstance(data, dict) or not isinstance(data.get('error'), dict):
        return False
    error = data['error']
    code = error.get('code')
    known = {'invalid_totp', 'invalid_totp_code', 'invalid_otp', 'invalid_code',
             'mfa_invalid_code', 'invalid_mfa_code', 'incorrect_code', 'wrong_code',
             'expired_code', 'totp_code_expired', 'mfa_code_expired'}
    if code in known:
        return True
    # Only an explicit incorrect/expired-code message enables a second try.
    message = str(error.get('message', '')).strip().lower()
    return code in {None, '', 'invalid_request_error'} and bool(re.fullmatch(
        r'(?:the )?(?:verification |authentication |security |totp )?code(?: you entered)? (?:is |was )?(?:incorrect|invalid|expired)\.?(?: (?:please )?try again\.?)?|(?:incorrect|invalid|expired) (?:verification |authentication |security |totp )?code\.?(?: (?:please )?try again\.?)?', message))


async def submit_totp_with_retry(page, otp_input, secret, replace_input, submit_form):
    state = _login_failure.get()
    if state is None:
        raise LoginBootstrapError('login_state_invalid')
    state['mfa_event'] = asyncio.Event()
    previous_code = None
    for attempt in range(2):
        state.pop('mfa_response', None)
        state['mfa_event'].clear()
        report_phase('mfa_retry' if attempt else 'mfa')
        code = await fresh_totp(secret, previous_code)
        if code == previous_code:
            raise LoginBootstrapError('login_mfa_retry_exhausted')
        await replace_input(otp_input, code, delay=60)
        await submit_form(otp_input, ('button[type="submit"]', 'button:has-text("Continue")', 'button:has-text("Verify")'), step='MFA')
        try:
            await asyncio.wait_for(state['mfa_event'].wait(), timeout=25)
        except asyncio.TimeoutError:
            raise LoginBootstrapError('login_session_incomplete') from None
        response = state['mfa_response']
        if response.status == 200:
            state.pop('code', None)
            report_phase('session')
            return
        retryable = False
        if state.get('code') == 'login_mfa_rejected' and response.status in {400,401,403}:
            try:
                data = await response.json()
                error = data.get('error') if isinstance(data, dict) else None
                if isinstance(error, dict) and error.get('code') in {'account_deactivated', 'user_deactivated'}:
                    state['code'] = 'account_die'
                else:
                    retryable = totp_error_is_retryable(data)
            except Exception:
                pass
        if not retryable:
            raise_login_failure()
            raise LoginBootstrapError('login_mfa_rejected')
        if attempt == 1:
            state['code'] = 'login_mfa_retry_exhausted'
            raise_login_failure()
        # Clear immediately after the rejection, then wait for a different code.
        # The same secret/time window cannot produce a new TOTP value.
        await otp_input.fill('')
        await otp_input.wait_for(state='visible', timeout=5000)
        previous_code = code
        state.pop('code', None)
        state['mfa_retry_authorized'] = True


def load_browser_settings():
    return _settings.get()


def login_request_allowed(url: str, method: str, main_navigation: bool) -> bool:
    parsed = urlsplit(url)
    if main_navigation and (parsed.scheme != 'https' or parsed.hostname not in {'auth.openai.com', 'chatgpt.com'}):
        return False
    if parsed.hostname == 'chatgpt.com' and parsed.path.startswith('/backend-api/accounts/mfa') and method.upper() not in {'GET', 'HEAD'}:
        return False
    return True


async def navigate_to_authorize(page, url, **_kwargs):
    validate_authorize_url(url)
    report = phase_reporter()
    failure = _login_failure.get()
    submissions = failure.setdefault('submissions', {}) if failure is not None else {}
    page.on('response', lambda response: record_login_response(response, failure))

    async def boundary(route):
        request = route.request
        main_navigation = request.is_navigation_request() and request.frame == page.main_frame
        if not login_request_allowed(request.url, request.method, main_navigation):
            await route.abort()
            return
        parsed = urlsplit(request.url)
        if (parsed.hostname == 'auth.openai.com' and request.method == 'POST'
                and parsed.path in {'/api/accounts/password/verify', '/api/accounts/mfa/verify'}):
            submissions[parsed.path] = submissions.get(parsed.path, 0) + 1
            if submissions[parsed.path] > 1:
                allowed_retry = (parsed.path == '/api/accounts/mfa/verify'
                                 and submissions[parsed.path] == 2 and failure is not None
                                 and failure.pop('mfa_retry_authorized', False))
                if not allowed_retry:
                    await route.abort()
                    return
        if parsed.hostname == 'auth.openai.com' and request.method == 'POST':
            stage = {'/api/accounts/password/verify': 'password', '/api/accounts/workspace/select': 'workspace'}.get(parsed.path)
            if stage:
                report(stage)
        await route.continue_()

    await page.context.route('**/*', boundary)
    response = await page.goto(url, wait_until='domcontentloaded', timeout=45000)
    if response is None:
        raise LoginBootstrapError('login_bootstrap_rejected')
    validate_oauth_response(SimpleNamespace(status_code=response.status, headers=response.headers, url=page.url))


async def get_browser_session(*, email, password, secret=None, proxy=None, log=None):
    report_phase('login_start')
    from config import Settings
    from session_phase import get_session
    if proxy is not None:
        raise LoginBootstrapError('login_bootstrap_rejected')
    with tempfile.TemporaryDirectory(prefix='twofa-login-') as directory:
        root = Path(directory)
        settings = Settings(root_dir=root, runtime_dir=root, browser_engine='camoufox',
                            browser_headless=True, browser_use_profile_template=False,
                            browser_profile_template_dir=root/'empty-template',
                            browser_camoufox_profile_dir=root/'empty-camoufox-template')
        token = _settings.set(settings)
        failure_token = _login_failure.set({})
        try:
            session = await get_session(email=email, password=password, secret=secret,
                                        headless=True, log=lambda _: None)
            if (not isinstance(session, dict) or not isinstance(session.get('accessToken'), str)
                    or not session['accessToken'].strip()
                    or not isinstance(session.get('user'), dict)
                    or str(session['user'].get('email', '')).strip().casefold() != email.strip().casefold()
                    or not isinstance(session.get('__cookies'), list) or not session['__cookies']):
                raise LoginBootstrapError('login_failed')
            return session
        except (asyncio.CancelledError, LoginBootstrapError):
            raise
        except Exception as exc:
            raise_login_failure()
            raise LoginBootstrapError(browser_failure_code(exc)) from None
        finally:
            _settings.reset(token)
            _login_failure.reset(failure_token)
