import asyncio
import os
from pathlib import Path
from types import SimpleNamespace

import pytest

from browser_login import browser_failure_code, get_browser_session, is_workspace_page, load_browser_settings, login_request_allowed, navigate_to_authorize, select_personal_workspace
from login_guard import LoginBootstrapError


@pytest.mark.parametrize('url,method,navigation,allowed', [
    ('https://auth.openai.com/log-in', 'GET', True, True),
    ('https://chatgpt.com/api/auth/callback/openai', 'GET', True, True),
    ('https://auth.openai.com.attacker.test/log-in', 'GET', True, False),
    ('http://auth.openai.com/log-in', 'GET', True, False),
    ('https://chatgpt.com/backend-api/accounts/mfa/user/disable_in_house', 'POST', False, False),
    ('https://chatgpt.com/backend-api/accounts/mfa/enroll', 'POST', False, False),
    ('https://chatgpt.com/backend-api/accounts/mfa_info', 'GET', False, True),
    ('https://auth.openai.com/api/accounts/mfa/verify', 'POST', False, True),
])
def test_login_network_boundary(url, method, navigation, allowed):
    assert login_request_allowed(url, method, navigation) is allowed


def test_browser_challenge_stops_and_duplicate_submissions_are_blocked():
    handlers = []
    async def route(_pattern, handler): handlers.append(handler)
    async def goto(_url, **_kwargs):
        return SimpleNamespace(status=403, headers={'cf-mitigated': 'challenge'})
    page = SimpleNamespace(context=SimpleNamespace(route=route), on=lambda *_args: None, goto=goto, url='https://auth.openai.com/log-in', main_frame=object())
    async def run():
        with pytest.raises(LoginBootstrapError, match='login_browser_challenge'):
            await navigate_to_authorize(page, 'https://auth.openai.com/api/accounts/authorize?state=offline')
        calls = []
        async def abort(): calls.append('abort')
        async def proceed(): calls.append('continue')
        request = SimpleNamespace(url='https://auth.openai.com/api/accounts/password/verify', method='POST', is_navigation_request=lambda: False)
        r = SimpleNamespace(request=request, abort=abort, continue_=proceed)
        await handlers[0](r)
        await handlers[0](r)
        assert calls == ['continue', 'abort']
    asyncio.run(run())


@pytest.mark.skipif(not os.getenv('CHANGE2FA_SOURCE_DIR'), reason='pinned engine source not supplied')
@pytest.mark.parametrize('outcome', ['success', 'wrong_account', 'no_cookies', 'exception', 'cancel'])
def test_profiles_are_isolated_cleaned_and_results_are_verified(monkeypatch, outcome):
    import session_phase
    roots = []
    async def fake(**kwargs):
        settings = load_browser_settings()
        roots.append(settings.runtime_dir)
        assert settings.runtime_dir.is_dir()
        assert not settings.browser_use_profile_template
        assert kwargs['headless'] is True
        (settings.runtime_dir/'private-profile').write_text('test-private-data')
        if outcome == 'exception': raise RuntimeError('sensitive response body')
        if outcome == 'cancel': raise asyncio.CancelledError()
        return {'accessToken': 'offline-token', 'user': {'email': 'other@example.com' if outcome == 'wrong_account' else kwargs['email']}, '__cookies': [] if outcome == 'no_cookies' else [{'name': 'session', 'value': 'offline'}]}
    monkeypatch.setattr(session_phase, 'get_session', fake)
    async def login():
        return await get_browser_session(email='test@example.com', password='private', secret='JBSWY3DPEHPK3PXP')
    if outcome == 'success':
        assert asyncio.run(login())['accessToken'] == 'offline-token'
        assert asyncio.run(login())['accessToken'] == 'offline-token'
        assert roots[0] != roots[1]
    elif outcome == 'cancel':
        with pytest.raises(asyncio.CancelledError): asyncio.run(login())
    else:
        with pytest.raises(LoginBootstrapError, match='login_failed') as error:
            asyncio.run(login())
        assert 'sensitive' not in str(error.value)
    assert all(not p.exists() for p in roots)
    with pytest.raises(LookupError): load_browser_settings()


@pytest.mark.skipif(not os.getenv('CHANGE2FA_SOURCE_DIR'), reason='pinned engine source not supplied')
def test_engine_selection_and_explicit_test_injection(monkeypatch):
    from rotation_service import StagedRotationService
    monkeypatch.setenv('TWOFA_LOGIN_ENGINE', 'camoufox')
    assert StagedRotationService().login_fn is get_browser_session
    async def injected(**_kwargs): return {}
    assert StagedRotationService(login_fn=injected).login_fn is injected
    monkeypatch.setenv('TWOFA_LOGIN_ENGINE', 'http')
    assert StagedRotationService().login_fn is not get_browser_session
    monkeypatch.setenv('TWOFA_LOGIN_ENGINE', 'unknown')
    with pytest.raises(ValueError): StagedRotationService()


@pytest.mark.skipif(not os.getenv('CHANGE2FA_SOURCE_DIR'), reason='pinned engine source not supplied')
def test_pinned_browser_import_uses_complete_private_bridge():
    import session_phase
    text = Path(session_phase.__file__).read_text()
    block = text.split('async def _get_session_browser(', 1)[1].split('async def get_session(', 1)[0]
    assert 'from browser_login import load_browser_settings' in block
    assert 'from browser_login import navigate_to_authorize as _navigate_to_authorize' in block
    assert 'from browser_phase import _navigate_to_authorize' not in block
    assert 'engine_order = ("camoufox",)' in block
    assert 'await select_personal_workspace(page)' in block
    assert 'workspace_selected = True' in block


@pytest.mark.parametrize('code', ['login_interaction_required', 'login_access_denied', 'login_rate_limited', 'login_bootstrap_rejected', 'login_workspace_selection_failed', 'login_session_incomplete'])
def test_browser_engine_wrapper_preserves_typed_guard_code(monkeypatch, code):
    import session_phase
    async def wrapped(**_kwargs):
        try:
            raise LoginBootstrapError(code)
        except LoginBootstrapError as exc:
            raise session_phase.SessionError('browser launch/driver error: private-cookie private-token') from exc
    monkeypatch.setattr(session_phase, 'get_session', wrapped)
    with pytest.raises(LoginBootstrapError) as caught:
        asyncio.run(get_browser_session(email='synthetic@example.com', password='private-password', secret='JBSWY3DPEHPK3PXP'))
    assert caught.value.code == code and str(caught.value) == code


def test_error_chain_never_trusts_arbitrary_text_or_loops():
    text = RuntimeError('server response: login_interaction_required private-token')
    assert browser_failure_code(text) == 'login_failed'
    text.__cause__ = text
    assert browser_failure_code(text) == 'login_failed'
    unknown = LoginBootstrapError('private-cookie')
    text.__cause__ = unknown
    assert browser_failure_code(text) == 'login_failed'
    known = LoginBootstrapError('login_rate_limited')
    unknown.__context__ = known
    assert browser_failure_code(text) == 'login_rate_limited'


@pytest.mark.parametrize('url,valid', [
    ('https://auth.openai.com/workspace', True),
    ('https://auth.openai.com/workspace/?state=not-logged', True),
    ('https://auth.openai.com.attacker.test/workspace', False),
    ('https://auth.openai.com:bad/workspace', False),
    ('http://auth.openai.com/workspace', False),
    ('https://auth.openai.com/other', False),
])
def test_workspace_page_requires_exact_trusted_origin(url, valid):
    assert is_workspace_page(url) is valid


@pytest.mark.parametrize('mode', ['one_personal', 'two_personal', 'none', 'external_link', 'navigation_during_click'])
def test_workspace_selects_only_one_explicit_personal_control(monkeypatch, mode):
    from unittest.mock import AsyncMock
    import browser_login
    async def sleep(_seconds): pass
    monkeypatch.setattr(browser_login.asyncio, 'sleep', sleep)
    click = AsyncMock(side_effect=RuntimeError('navigation interrupted') if mode == 'navigation_during_click' else None)
    item = SimpleNamespace(is_visible=AsyncMock(return_value=True), click=click,
                           get_attribute=AsyncMock(return_value='https://external.test/' if mode == 'external_link' else None))
    empty = SimpleNamespace(count=AsyncMock(return_value=0))
    choices = SimpleNamespace(count=AsyncMock(return_value=2 if mode == 'two_personal' else 0 if mode == 'none' else 1), nth=lambda _i:item)
    page = SimpleNamespace(url='https://auth.openai.com/workspace',
                           get_by_role=lambda role, **_kwargs: choices if role=='button' else empty,
                           get_by_text=lambda *_args:SimpleNamespace(locator=lambda _selector:empty))
    if mode in {'one_personal','navigation_during_click'}:
        asyncio.run(select_personal_workspace(page));assert click.await_count==1
    else:
        with pytest.raises(LoginBootstrapError, match='login_workspace_selection_failed'):
            asyncio.run(select_personal_workspace(page))
        assert click.await_count==0


def test_cookie_wait_timeout_is_not_reported_as_bad_credentials():
    assert browser_failure_code(RuntimeError('timeout waiting session cookies. URL: private-state')) == 'login_session_incomplete'


def test_browser_failure_distinguishes_email_code_from_missing_totp_control():
    from browser_login import browser_failure_code
    assert browser_failure_code(RuntimeError('email verification required')) == 'login_email_verification_required'
    assert browser_failure_code(RuntimeError('totp input not found')) == 'login_interaction_required'


@pytest.mark.parametrize('path,status,headers,expected', [
    ('/api/accounts/mfa/verify',403,{},'login_mfa_rejected'),
    ('/api/accounts/password/verify',401,{},'login_password_rejected'),
    ('/api/accounts/mfa/verify',403,{'cf-mitigated':'challenge'},'login_browser_challenge'),
    ('/api/accounts/mfa/verify',429,{},'login_rate_limited'),
    ('/api/accounts/mfa/verify',500,{},'login_upstream_error'),
    ('/api/accounts/mfa/verify',200,{},None),
])
def test_rejected_login_phase_stops_without_waiting_for_cookie_timeout(path,status,headers,expected):
    import browser_login
    state = {}
    response = SimpleNamespace(url='https://auth.openai.com'+path, status=status, headers=headers, request=SimpleNamespace(method='POST'))
    browser_login.record_login_response(response, state)
    token = browser_login._login_failure.set(state)
    try:
        if expected:
            with pytest.raises(LoginBootstrapError, match=expected):
                browser_login.raise_login_failure()
        else:
            browser_login.raise_login_failure()
    finally:
        browser_login._login_failure.reset(token)


@pytest.mark.parametrize('remaining', [1,5,6,30])
def test_totp_is_generated_after_boundary_wait(monkeypatch, remaining):
    import browser_login, totp_helper
    events = []
    monkeypatch.setattr(totp_helper, 'time_remaining', lambda: remaining)
    async def sleep(seconds): events.append(('wait', seconds))
    def generate(secret):
        assert secret == 'offline-secret'
        events.append(('generate', None))
        return '123456'
    monkeypatch.setattr(browser_login.asyncio, 'sleep', sleep)
    monkeypatch.setattr(totp_helper, 'generate_code', generate)
    assert asyncio.run(browser_login.fresh_totp('offline-secret')) == '123456'
    assert events == ([('wait',remaining+0.2)] if remaining<=5 else []) + [('generate',None)]


@pytest.mark.parametrize('outcomes,expected_count,expected_error', [
    ([(200,{})], 1, None),
    ([(400,{'code':'invalid_code'}),(200,{})], 2, None),
    ([(400,{'code':'invalid_code'}),(400,{'code':'invalid_code'})], 2, 'login_mfa_retry_exhausted'),
    ([(403,{'code':'account_deactivated'})], 1, 'account_die'),
    ([(403,{'code':'access_denied'})], 1, 'login_mfa_rejected'),
    ([(429,{'code':'invalid_code'})], 1, 'login_rate_limited'),
])
def test_totp_clears_before_second_input_and_never_submits_a_third(monkeypatch,outcomes,expected_count,expected_error):
    import browser_login
    state = {};events=[];counter=0
    class Input:
        async def fill(self,value): events.append(('clear',value))
        async def wait_for(self,**kwargs): pass
    class Response:
        url='https://auth.openai.com/api/accounts/mfa/verify'
        headers={}
        request=SimpleNamespace(method='POST')
        def __init__(self,status,error):self.status=status;self.error=error
        async def json(self):return {'error':self.error}
    async def fresh(secret,previous=None):
        code = '123456' if previous is None else '654321'
        events.append(('generate',code));return code
    async def replace(_input,code,**kwargs):events.append(('input',code))
    async def submit(_input,_selectors,**kwargs):
        nonlocal counter
        counter+=1
        status,error=outcomes[counter-1]
        browser_login.record_login_response(Response(status,error),state)
    monkeypatch.setattr(browser_login,'fresh_totp',fresh)
    token=browser_login._login_failure.set(state)
    try:
        operation=browser_login.submit_totp_with_retry(SimpleNamespace(),Input(),'offline-secret',replace,submit)
        if expected_error:
            with pytest.raises(LoginBootstrapError,match=expected_error):asyncio.run(operation)
        else:asyncio.run(operation)
    finally:browser_login._login_failure.reset(token)
    assert counter==expected_count
    if expected_count==2:
        assert events==[('generate','123456'),('input','123456'),('clear',''),('generate','654321'),('input','654321')]
    else:assert not any(event[0]=='clear' for event in events)


def test_fresh_totp_uses_current_code_immediately_when_previous_window_expired(monkeypatch):
    import browser_login,totp_helper
    monkeypatch.setattr(totp_helper,'time_remaining',lambda:20)
    monkeypatch.setattr(totp_helper,'generate_code',lambda _secret:'654321')
    async def unexpected_sleep(_seconds):raise AssertionError('new window must not wait')
    monkeypatch.setattr(browser_login.asyncio,'sleep',unexpected_sleep)
    assert asyncio.run(browser_login.fresh_totp('offline','123456'))=='654321'


def test_only_one_explicit_retry_passes_network_boundary(monkeypatch):
    import browser_login
    handlers=[];state={};actions=[]
    async def route(_pattern,handler):handlers.append(handler)
    async def goto(_url,**kwargs):return SimpleNamespace(status=200,headers={})
    page=SimpleNamespace(context=SimpleNamespace(route=route),on=lambda *_args:None,goto=goto,url='https://auth.openai.com/log-in',main_frame=object())
    class Route:
        request=SimpleNamespace(url='https://auth.openai.com/api/accounts/mfa/verify',method='POST',is_navigation_request=lambda:False)
        async def abort(self):actions.append('abort')
        async def continue_(self):actions.append('send')
    async def run():
        await browser_login.navigate_to_authorize(page,'https://auth.openai.com/authorize?state=offline')
        await handlers[-1](Route())
        state['mfa_retry_authorized']=True
        await handlers[-1](Route())
        state['mfa_retry_authorized']=True
        await handlers[-1](Route())
        # Opening another page cannot reset the attempt counter.
        await browser_login.navigate_to_authorize(page,'https://auth.openai.com/authorize?state=offline')
        await handlers[-1](Route())
    token=browser_login._login_failure.set(state)
    try:asyncio.run(run())
    finally:browser_login._login_failure.reset(token)
    assert actions==['send','send','abort','abort']
