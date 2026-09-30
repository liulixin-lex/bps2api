"""Offline bootstrap and phase tests. No real accounts or network."""
import asyncio
import json
import os
from pathlib import Path
from types import SimpleNamespace

import pytest

from login_guard import LoginBootstrapError, validate_authorize_url, validate_oauth_response
from rotation_service import PREFIX, StagedRotationService, pre_rotation_failure


@pytest.mark.parametrize('url', [
    'http://auth.openai.com/authorize?state=x',
    'https://chatgpt.com/api/auth/signin?csrf=true',
    'https://auth.openai.com.attacker.test/authorize?state=x',
    'https://user:pass@auth.openai.com/authorize?state=x',
    'https://auth.openai.com:444/authorize?state=x',
    'https://auth.openai.com/authorize',
    'https://auth.openai.com/authorize?state=x&state=y',
    'https://auth.openai.com/authorize?state=x#fragment',
])
def test_invalid_authorize_url_never_proceeds(url):
    with pytest.raises(LoginBootstrapError) as error:
        validate_authorize_url(url)
    assert str(error.value) == 'login_bootstrap_rejected'


def test_valid_authorize_url():
    validate_authorize_url('https://auth.openai.com/api/accounts/authorize?state=opaque')


@pytest.mark.parametrize('status,headers,url,code', [
    (403, {}, 'https://auth.openai.com/api/accounts/authorize', 'login_access_denied'),
    (403, {'cf-mitigated': 'challenge'}, 'https://auth.openai.com/api/accounts/authorize', 'login_browser_challenge'),
    (403, {'cf-mitigated': ' CHALLENGE '}, 'https://auth.openai.com/api/accounts/authorize', 'login_browser_challenge'),
    (403, {'cf-mitigated': 'unknown-sensitive-value'}, 'https://auth.openai.com/api/accounts/authorize', 'login_access_denied'),
    (429, {}, 'https://auth.openai.com/api/accounts/authorize', 'login_rate_limited'),
    (500, {}, 'https://auth.openai.com/api/accounts/authorize', 'login_bootstrap_rejected'),
    (200, {'cf-mitigated': 'challenge'}, 'https://auth.openai.com/log-in', 'login_browser_challenge'),
    (200, {}, 'https://chatgpt.com/api/auth/error?secret=private', 'login_bootstrap_rejected'),
])
def test_oauth_rejection_stops_before_next_step(status, headers, url, code):
    with pytest.raises(LoginBootstrapError) as error:
        validate_oauth_response(SimpleNamespace(status_code=status, headers=headers, url=url))
    assert str(error.value) == code
    assert 'private' not in str(error.value)


def test_valid_oauth_landing():
    validate_oauth_response(SimpleNamespace(status_code=200, headers={}, url='https://auth.openai.com/log-in/password'))


@pytest.mark.skipif(not os.getenv('CHANGE2FA_SOURCE_DIR'), reason='pinned engine source not supplied')
@pytest.mark.parametrize('challenge,code', [(False, 'login_access_denied'), (True, 'login_browser_challenge')])
def test_bootstrap_403_never_calls_authorize_continue(monkeypatch, challenge, code):
    import request_phase
    import session_phase
    count = {'continue': 0, 'bootstrap': 0}
    class Session:
        cookies = SimpleNamespace(jar=[], set=lambda *_a, **_k: None)
        def get(self, url, **_kwargs):
            denied = url.startswith('https://auth.openai.com/')
            count['bootstrap'] += int(denied)
            return SimpleNamespace(status_code=403 if denied else 200, url=url,
                                   headers={'cf-mitigated': 'challenge'} if denied and challenge else {})
        def post(self, _url, **_kwargs):
            return SimpleNamespace(status_code=200, json=lambda: {'url': 'https://auth.openai.com/api/accounts/authorize?state=offline'})
        def close(self): pass
    def unexpected_continue(*_a, **_k):
        count['continue'] += 1
        raise AssertionError('Continued after denied bootstrap')
    monkeypatch.setattr(request_phase, '_create_session', lambda **_k: Session())
    monkeypatch.setattr(request_phase, '_step_csrf', lambda *_a: 'offline-csrf')
    monkeypatch.setattr(request_phase, '_step_authorize_continue', unexpected_continue)
    monkeypatch.setattr(request_phase, '_get_sentinel_token', lambda *_a: 'offline')
    monkeypatch.setattr(session_phase, '_resolve_login_flow', lambda _explicit: 'anti409')
    with pytest.raises(LoginBootstrapError, match=code):
        asyncio.run(session_phase.get_session_pure_request(email='test@example.com', password='private', secret='JBSWY3DPEHPK3PXP', log=lambda _s: None))
    assert count == {'continue': 0, 'bootstrap': 1}


@pytest.mark.skipif(not os.getenv('CHANGE2FA_SOURCE_DIR'), reason='pinned engine source not supplied')
def test_initial_login_failure_is_explicit_and_never_rotates():
    from service import TwoFAFlowError
    calls = {'login': 0, 'rotate': 0}
    async def login(**_kwargs):
        calls['login'] += 1
        raise LoginBootstrapError('login_access_denied')
    async def rotate(**_kwargs):
        calls['rotate'] += 1
        raise AssertionError('Rotation must not execute')
    service = StagedRotationService(login_fn=login, rotate_fn=rotate)
    async def checkpoint(_secret): raise AssertionError('No new secret')
    with pytest.raises(TwoFAFlowError) as error:
        asyncio.run(service.rotate(email='test@example.com', password='private', old_secret='JBSWY3DPEHPK3PXP', timeout=1, checkpoint=checkpoint, log=lambda _s: None))
    assert error.value.error_kind == PREFIX + 'login_access_denied'
    assert str(error.value) == 'login_access_denied'
    assert calls == {'login': 1, 'rotate': 0}


@pytest.mark.skipif(not os.getenv('CHANGE2FA_SOURCE_DIR'), reason='pinned engine source not supplied')
@pytest.mark.parametrize('after_checkpoint', [False, True])
def test_mutation_or_post_rotation_login_failure_remains_uncertain(after_checkpoint):
    from service import TwoFAFlowError
    calls = {'login': 0, 'rotate': 0, 'checkpoint': 0}
    async def login(**_kwargs):
        calls['login'] += 1
        if calls['login'] > 1: raise LoginBootstrapError('login_access_denied')
        return {'accessToken': 'offline-token'}
    async def entitlement(**_kwargs): return {'plan': 'plus'}
    async def rotate(**_kwargs):
        calls['rotate'] += 1
        if not after_checkpoint: raise LoginBootstrapError('login_access_denied')
        return {'secret': 'KRUGS4ZANFZSAYJA', 'activated': True}
    async def checkpoint(_secret): calls['checkpoint'] += 1
    service = StagedRotationService(login_fn=login, rotate_fn=rotate, entitlement_fn=entitlement)
    with pytest.raises(TwoFAFlowError) as error:
        asyncio.run(service.rotate(email='test@example.com', password='private', old_secret='JBSWY3DPEHPK3PXP', timeout=1, checkpoint=checkpoint, log=lambda _s: None))
    assert not error.value.error_kind.startswith(PREFIX)
    assert calls['rotate'] == 1
    assert calls['checkpoint'] == int(after_checkpoint)


def test_legacy_projection_is_limited_and_does_not_mutate_record():
    job = SimpleNamespace(status='error', rotated_pending_verify=False, login_verified=False,
                          password_changed=False, retry_count=0, error_kind='technical_error',
                          error='Đăng nhập thất bại: authorize/continue HTTP 409 invalid_state')
    original = dict(vars(job))
    assert pre_rotation_failure(job) == 'login_state_invalid'
    assert vars(job) == original
    for field, value in [('rotated_pending_verify', True), ('password_changed', True),
                         ('retry_count', 1), ('error_kind', 'rotation_interrupted'),
                         ('error', 'Đổi 2FA thất bại: HTTP 409 invalid_state')]:
        changed = SimpleNamespace(**{**original, field: value})
        assert pre_rotation_failure(changed) is None


@pytest.mark.skipif(not os.getenv('CHANGE2FA_SOURCE_DIR'), reason='pinned engine source not supplied')
def test_staged_failure_persists_and_successful_rotation_still_verifies(tmp_path):
    import sqlite3
    from cryptography.fernet import Fernet
    from db import get_engine, get_repos, get_settings_repo
    from jobs import TwoFAJobManager
    from worker import EncryptedJobRepository, Runtime
    calls = {'rotate': 0}
    async def rejected_login(**_kwargs): raise LoginBootstrapError('login_access_denied')
    async def login(**_kwargs): return {'accessToken': 'offline', 'accountPlan': 'plus'}
    async def entitlement(**_kwargs): return {'plan': 'plus'}
    async def rotate(**_kwargs):
        calls['rotate'] += 1
        return {'secret': 'KRUGS4ZANFZSAYJA', 'activated': True}
    engine = get_engine(str(tmp_path / 'state.db'))
    raw = get_repos(engine)[1]
    encrypted = EncryptedJobRepository(raw, Fernet(Fernet.generate_key()))
    service = StagedRotationService(login_fn=rejected_login, rotate_fn=rotate, entitlement_fn=entitlement)
    manager = TwoFAJobManager(encrypted, get_settings_repo(engine), service=service)
    manager.parse_combo = lambda _line: ('test@example.com', 'private-password', 'JBSWY3DPEHPK3PXP')
    job_id = manager.add(['structured'], 'change_2fa')[0]['id']
    asyncio.run(manager._run(manager.jobs[job_id]))
    assert calls['rotate'] == 0
    recovered = TwoFAJobManager(encrypted, get_settings_repo(engine), service=service)
    rt = Runtime(recovered, sqlite3.connect(':memory:'), b'offline-key')
    summary = rt.summary(recovered.jobs[job_id])
    assert summary['status'] == 'login_failed' and summary['error_code'] == 'login_access_denied'
    assert not recovered._queue.qsize()
    assert 'private-password' not in json.dumps(raw.list_all())
    good = StagedRotationService(login_fn=login, rotate_fn=rotate, entitlement_fn=entitlement)
    manager.service = good
    second = manager.add(['structured'], 'change_2fa')[0]['id']
    asyncio.run(manager._run(manager.jobs[second]))
    assert manager.jobs[second].status == 'success' and manager.jobs[second].login_verified
    assert calls['rotate'] == 1
    rt.requests.close(); engine.close()
