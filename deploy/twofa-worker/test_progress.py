import asyncio
from contextvars import Context

from progress import phase_reporter, report_phase, track_progress, verification_progress


def test_progress_is_allowlisted_scoped_and_safe_for_foreign_callbacks():
    events = []
    with track_progress(events.append):
        report_phase('password')
        report_phase('private-cookie')
        with verification_progress():
            callback = phase_reporter()
            Context().run(callback, 'mfa')
        report_phase('preflight')
    callback('password')
    report_phase('rotating')
    assert events == ['password', 'verify_mfa', 'preflight']


def test_progress_does_not_cross_concurrent_tasks():
    async def scenario():
        async def task(verify):
            events = []
            with track_progress(events.append), verification_progress(verify):
                await asyncio.sleep(0)
                report_phase('mfa')
            return events
        assert await asyncio.gather(task(False), task(True)) == [['mfa'], ['verify_mfa']]
    asyncio.run(scenario())


def test_live_manager_reports_initial_and_new_secret_verification(tmp_path, monkeypatch):
    import os
    from cryptography.fernet import Fernet
    from worker import build_runtime, RotationInput
    from rotation_service import StagedRotationService
    monkeypatch.setenv('TWOFA_DATA_KEY', Fernet.generate_key().decode())
    monkeypatch.setenv('TWOFA_DATA_DIR', str(tmp_path))
    assert os.environ.get('CHANGE2FA_SOURCE_DIR')
    async def scenario():
        rt, engine = build_runtime()
        observed = []
        async def login(**kwargs):
            report_phase('mfa')
            observed.append(rt.summary(next(iter(rt.manager.jobs.values()))))
            return {'accessToken': 'synthetic', 'user': {'email': kwargs['email']}}
        async def rotate(**kwargs):
            observed.append(rt.summary(next(iter(rt.manager.jobs.values()))))
            return {'secret': 'JBSWY3DPEHPK3PXP', 'activated': True}
        async def entitlement(**kwargs): return {'plan': 'plus'}
        rt.manager.service = StagedRotationService(login_fn=login, rotate_fn=rotate, entitlement_fn=entitlement)
        rt.manager.start()
        try:
            await rt.submit(RotationInput(email='synthetic@example.com', password='test-only', mfa_secret='JBSWY3DPEHPK3PXP', request_id='a'*32, confirmed=True))
            await asyncio.wait_for(rt.manager._queue.join(), 5)
            assert [j['phase'] for j in observed] == ['mfa', 'rotating', 'verify_mfa']
            assert all(j['status'] == 'running' and j['phase_started_at'] >= j['started_at'] > 0 for j in observed)
            final = rt.summary(next(iter(rt.manager.jobs.values())))
            assert final['status'] == 'success' and 'phase' not in final
            assert rt.manager.progress == {}
        finally:
            await rt.manager.shutdown()
            rt.requests.close()
            engine.close()
            rt.lock_file.close()
    asyncio.run(scenario())
