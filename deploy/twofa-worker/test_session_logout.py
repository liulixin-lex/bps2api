import asyncio
import copy
import json
import sqlite3
import uuid
from pathlib import Path
from types import SimpleNamespace

import pytest
from fastapi.testclient import TestClient
from login_guard import LoginBootstrapError
from session_logout import SessionLogoutRuntime, LogoutDispatch, LogoutError, is_logout_write
from worker import RotationInput, Runtime, create_app

TOKEN = 'test-session-worker-token-' * 3
SECRET = 'JBSWY3DPEHPK3PXP'
PASSWORD = ' p|a,ss$[]+----word '


def entry(**overrides):
    return {'email': 'test@example.com', 'password': PASSWORD, 'mfa_secret': SECRET,
            'request_id': uuid.uuid4().hex, 'confirmed': True, **overrides}


async def login(**kwargs):
    return {'user': {'email': kwargs['email']}, 'accessToken': 'private-token',
            '__cookies': [{'name': 'session', 'value': 'private-cookie'}]}


@pytest.fixture
def runtime():
    manager = SimpleNamespace(jobs={}, order=[])
    rt = Runtime(manager, sqlite3.connect(':memory:', check_same_thread=False), b'test-key')
    rt.session_logout.login_fn = login
    yield rt
    rt.requests.close()


@pytest.fixture
def client(runtime):
    with TestClient(create_app(runtime, TOKEN), raise_server_exceptions=False) as client:
        client.headers['Authorization'] = 'Bearer ' + TOKEN
        yield client


def settle(client):
    for _ in range(100):
        jobs = client.get('/session-logout/jobs').json()['jobs']
        if jobs and jobs[0]['status'] not in {'queued', 'logging_in', 'revoking'}:
            return jobs[0]
    raise AssertionError('job did not finish')


def test_logout_authentication_and_confirmation_without_echo(client):
    assert client.get('/session-logout/jobs', headers={'Authorization': ''}).status_code == 401
    for payload in [entry(confirmed=False), entry(confirmed='yes'), entry(mfa_secret='private-invalid'), entry(extra='private')]:
        response = client.post('/session-logout/jobs', json=payload)
        assert response.status_code in {400, 422}
        assert PASSWORD not in response.text and SECRET not in response.text
        assert response.headers['cache-control'] == 'no-store'
    assert client.get('/session-logout/jobs').json() == {'jobs': []}


def test_acknowledged_logout_is_idempotent_and_stores_no_credentials(client, runtime):
    calls = []
    async def revoke(session, email, before_send):
        calls.append(email)
        assert session['accessToken'] == 'private-token'
        await before_send()
        return True
    runtime.session_logout.revoke_fn = revoke
    data = entry()
    first = client.post('/session-logout/jobs', json=data)
    assert first.status_code == 202
    result = settle(client)
    assert result['status'] == 'accepted'
    repeated = client.post('/session-logout/jobs', json=data)
    assert repeated.json()['id'] == first.json()['id'] and len(calls) == 1
    assert client.post('/session-logout/jobs', json={**data, 'password': 'different'}).status_code == 409
    dump = '\n'.join(runtime.requests.iterdump())
    output = client.get('/session-logout/jobs').text
    for value in [PASSWORD, SECRET, 'private-token', 'private-cookie']:
        assert value not in dump and value not in output
    assert set(result) == {'id', 'email', 'status', 'error_code', 'created_at', 'finished_at'}
    assert client.get('/jobs').json()['jobs'] == []


@pytest.mark.parametrize('mode,status,code,sent', [
    ('challenge', 'login_failed', 'login_interaction_required', False),
    ('wrong_account', 'login_failed', 'identity_mismatch', False),
    ('control_missing', 'failed', 'logout_control_missing', False),
    ('false_ack', 'failed', 'logout_unconfirmed', False),
    ('lost_response', 'needs_review', 'logout_unconfirmed', True),
    ('rejected', 'needs_review', 'logout_rejected', True),
])
def test_failures_never_claim_logout_or_replay(client, runtime, mode, status, code, sent):
    calls = []
    async def guarded_login(**kwargs):
        if mode == 'challenge': raise LoginBootstrapError('login_interaction_required')
        result = await login(**kwargs)
        if mode == 'wrong_account': result['user']['email'] = 'different@example.com'
        return result
    async def revoke(session, email, before_send):
        calls.append(email)
        if mode == 'control_missing': raise LogoutError('logout_control_missing')
        if mode == 'false_ack': return True
        await before_send()
        if mode == 'rejected': raise LogoutError('logout_rejected')
        raise RuntimeError('private-token and private-cookie')
    runtime.session_logout.login_fn = guarded_login
    runtime.session_logout.revoke_fn = revoke
    data = entry(); assert client.post('/session-logout/jobs', json=data).status_code == 202
    result = settle(client)
    assert result['status'] == status and result['error_code'] == code
    assert 'private-' not in json.dumps(result)
    assert len(calls) == (0 if mode in {'challenge', 'wrong_account'} else 1)
    assert client.post('/session-logout/jobs', json=data).json()['id'] == result['id']
    if sent: assert client.post('/session-logout/jobs', json=entry()).status_code == 409


def test_rotation_and_logout_cannot_overlap(client, runtime):
    runtime.manager.jobs['rotation'] = SimpleNamespace(status='queued')
    assert client.post('/session-logout/jobs', json=entry()).status_code == 409
    assert runtime.session_logout.list_jobs() == []
    runtime.manager.jobs.clear()
    async def waiting_login(**_kwargs): await asyncio.Event().wait()
    runtime.session_logout.login_fn = waiting_login
    assert client.post('/session-logout/jobs', json=entry()).status_code == 202
    assert client.post('/session-logout/jobs', json=entry(email='other@example.com')).status_code == 409
    assert client.post('/jobs', json=entry()).status_code == 409
    job = SimpleNamespace(id='abc', mode='change_2fa', status='error', rotated_pending_verify=True, retryable=True)
    runtime.manager.jobs[job.id] = job
    assert client.post('/jobs/abc/verify').status_code == 409


def test_restart_preserves_idempotency_and_never_resumes_credentials(tmp_path):
    path = tmp_path/'requests.db'
    rt = Runtime(SimpleNamespace(jobs={}, order=[]), sqlite3.connect(path), b'key')
    data = RotationInput(**entry())
    async def run():
        rt.session_logout.login_fn = lambda **kwargs: asyncio.Event().wait()
        job = await rt.session_logout.submit(data)
        # Shutdown can arrive before the newly scheduled coroutine starts.
        await rt.session_logout.shutdown()
        return job
    job = asyncio.run(run())
    assert rt.session_logout.list_jobs()[0]['status'] == 'interrupted'
    rt.requests.close()
    restored = Runtime(SimpleNamespace(jobs={}, order=[]), sqlite3.connect(path), b'key')
    assert asyncio.run(restored.session_logout.submit(data))['id'] == job['id']
    assert not restored.session_logout.tasks
    restored.requests.execute("UPDATE session_logout_jobs SET status='revoking'"); restored.requests.commit()
    recovered = SessionLogoutRuntime(restored)
    assert recovered.list_jobs()[0]['status'] == 'needs_review'
    assert not recovered.active()
    restored.requests.close()


@pytest.mark.parametrize('url,method,expected', [
    ('https://chatgpt.com/backend-api/auth/logout', 'POST', True),
    ('https://auth.openai.com/api/accounts/logout', 'POST', True),
    ('https://chatgpt.com/backend-api/auth/logout_all', 'POST', True),
    ('https://chatgpt.com/backend-api/session', 'POST', False),
    ('https://chatgpt.com/api/auth/signout', 'POST', False),
    ('https://chatgpt.com/backend-api/accounts/mfa/enroll', 'POST', False),
    ('https://chatgpt.com/backend-api/auth/logout', 'GET', False),
    ('https://chatgpt.com.attacker.test/backend-api/auth/logout', 'POST', False),
    ('http://chatgpt.com/backend-api/auth/logout', 'POST', False),
    ('https://chatgpt.com:bad/backend-api/auth/logout', 'POST', False),
])
def test_only_account_logout_responses_qualify(url, method, expected):
    assert is_logout_write(url, method) is expected


def test_dispatch_requires_explicit_action_durable_checkpoint_and_exact_response():
    async def run():
        calls = []
        async def before(): calls.append('persist')
        dispatch = LogoutDispatch(before)
        request = SimpleNamespace(url='https://chatgpt.com/backend-api/auth/logout', method='POST', is_navigation_request=lambda: False)
        async def abort(): calls.append('abort')
        async def send(): calls.append('send')
        route = SimpleNamespace(request=request, abort=abort, continue_=send)
        await dispatch.route(route)
        assert calls == ['abort']
        dispatch.armed = True
        await dispatch.route(route)
        await dispatch.route(route)
        assert calls == ['abort', 'persist', 'send', 'abort']
        dispatch.response(SimpleNamespace(request=copy.copy(request), status=200))
        assert not dispatch.accepted
        dispatch.response(SimpleNamespace(request=request, status=200))
        assert dispatch.accepted and dispatch.done.is_set()
    asyncio.run(run())


@pytest.mark.parametrize('mode', ['direct', 'dialog', 'identity_mismatch', 'missing_control', 'rejected'])
def test_native_browser_flow_checks_identity_confirmation_and_response(monkeypatch, mode):
    from unittest.mock import AsyncMock
    import session_logout
    calls = []
    profile = []
    empty = SimpleNamespace(count=AsyncMock(return_value=0))
    def locator(item):
        return SimpleNamespace(count=AsyncMock(return_value=1), nth=lambda _: item)
    class Context:
        async def __aenter__(self): return self
        async def __aexit__(self, *_args): calls.append('closed')
        async def route(self, _pattern, handler): self.handler = handler
        async def add_cookies(self, cookies): assert cookies[0]['value'] == 'private-cookie'
    context = Context()
    handlers = {}
    async def send():
        request = SimpleNamespace(url='https://chatgpt.com/backend-api/auth/logout_all', method='POST', is_navigation_request=lambda: False)
        async def abort(): calls.append('aborted')
        async def forward():
            calls.append('forwarded')
            handlers['response'](SimpleNamespace(request=copy.copy(request), status=200))
            handlers['response'](SimpleNamespace(request=request, status=500 if mode == 'rejected' else 204))
        await context.handler(SimpleNamespace(request=request, abort=abort, continue_=forward))
    async def click(**_kwargs):
        calls.append('all-button')
        if mode != 'dialog': await send()
    async def confirm_click(**_kwargs):
        calls.append('confirm-button'); await send()
    original = object()
    button = SimpleNamespace(is_visible=AsyncMock(return_value=True), element_handle=AsyncMock(return_value=original), click=click)
    confirm = SimpleNamespace(is_visible=AsyncMock(return_value=True), evaluate=AsyncMock(return_value=False), click=confirm_click)
    dialog = SimpleNamespace(get_by_role=lambda *_args, **_kwargs: locator(confirm))
    def roles(role, **_kwargs):
        if role == 'dialog': return locator(dialog) if mode == 'dialog' else empty
        return empty if mode == 'missing_control' else locator(button)
    def on(name, handler): handlers[name] = handler
    page = SimpleNamespace(goto=AsyncMock(), evaluate=AsyncMock(return_value='other@example.com' if mode == 'identity_mismatch' else 'test@example.com'),
                           get_by_role=roles, get_by_text=lambda *_args: empty, on=on,
                           remove_listener=lambda name, _handler: handlers.pop(name))
    context.pages = [page]
    def factory(**kwargs):
        profile.append(Path(kwargs['user_data_dir']))
        assert profile[-1].is_dir() and kwargs['persistent_context'] is True
        return context
    async def before(): calls.append('persist')
    async def no_delay(_seconds): pass
    monkeypatch.setattr(session_logout.asyncio, 'sleep', no_delay)
    session = {'__cookies': [{'name': 'session', 'value': 'private-cookie', 'domain': '.chatgpt.com', 'path': '/'}]}
    async def run():
        if mode in {'identity_mismatch', 'missing_control', 'rejected'}:
            with pytest.raises(LogoutError) as exc:
                await session_logout.revoke_browser_sessions(session, 'test@example.com', before, browser_factory=factory)
            assert exc.value.code == {'identity_mismatch': 'identity_mismatch', 'missing_control': 'logout_control_missing', 'rejected': 'logout_rejected'}[mode]
        else:
            assert await session_logout.revoke_browser_sessions(session, 'test@example.com', before, browser_factory=factory) is True
    asyncio.run(run())
    assert not profile[0].exists() and calls[-1] == 'closed' and not handlers
    if mode in {'identity_mismatch', 'missing_control'}:
        assert calls == ['closed']
    else:
        expected = ['all-button'] + (['confirm-button'] if mode == 'dialog' else [])
        assert calls == expected + ['persist', 'forwarded', 'closed']


def test_failed_durable_checkpoint_blocks_external_request():
    async def run():
        calls = []
        async def before(): raise RuntimeError('database unavailable')
        dispatch = LogoutDispatch(before);dispatch.armed = True
        request = SimpleNamespace(url='https://chatgpt.com/backend-api/auth/logout', method='POST')
        async def abort(): calls.append('abort')
        async def send(): calls.append('send')
        with pytest.raises(RuntimeError): await dispatch.route(SimpleNamespace(request=request, abort=abort, continue_=send))
        assert calls == ['abort'] and not dispatch.accepted
    asyncio.run(run())
