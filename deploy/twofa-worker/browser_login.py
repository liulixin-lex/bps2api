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

_settings = ContextVar('twofa_browser_settings')


def browser_failure_code(exc: BaseException) -> str:
    """Preserve typed guard errors wrapped by the pinned browser engine.

    The engine raises SessionError from LoginBootstrapError. Never parse a
    code out of arbitrary response text or expose the wrapper's diagnostics.
    """
    current = exc
    seen = set()
    allowed = {'login_bootstrap_rejected', 'login_interaction_required',
               'login_access_denied', 'login_rate_limited', 'login_failed',
               'invalid_credentials', 'account_die', 'login_workspace_selection_failed',
               'login_session_incomplete'}
    while current is not None and id(current) not in seen:
        seen.add(id(current))
        if isinstance(current, LoginBootstrapError) and current.code in allowed:
            return current.code
        current = current.__cause__ or current.__context__
    text = str(exc).lower()
    if 'email verification' in text or 'totp input not found' in text:
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
    submissions = {}

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
                await route.abort()
                return
        await route.continue_()

    await page.context.route('**/*', boundary)
    response = await page.goto(url, wait_until='domcontentloaded', timeout=45000)
    if response is None:
        raise LoginBootstrapError('login_bootstrap_rejected')
    validate_oauth_response(SimpleNamespace(status_code=response.status, headers=response.headers, url=page.url))


async def get_browser_session(*, email, password, secret=None, proxy=None, log=None):
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
            raise LoginBootstrapError(browser_failure_code(exc)) from None
        finally:
            _settings.reset(token)
