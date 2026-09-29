"""Unattended login using the pinned upstream browser flow.

Each login gets a disposable profile, including post-rotation verification.
This module never changes MFA settings or exposes a browser control port.
"""
import asyncio
from contextvars import ContextVar
from pathlib import Path
import tempfile
from types import SimpleNamespace
from urllib.parse import urlsplit

from login_guard import LoginBootstrapError, validate_authorize_url, validate_oauth_response

_settings = ContextVar('twofa_browser_settings')


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
            text = str(exc).lower()
            code = 'login_failed'
            if 'email verification' in text or 'totp input not found' in text:
                code = 'login_interaction_required'
            elif 'password error' in text:
                code = 'invalid_credentials'
            elif 'account deactivated' in text:
                code = 'account_die'
            raise LoginBootstrapError(code) from None
        finally:
            _settings.reset(token)
