"""Apply checked bootstrap guards and an isolated browser bridge."""
import hashlib
import sys
from pathlib import Path

EXPECTED_SHA256 = "a0c1f2446831a044a3e554e3c7c3c8869022002ffe3bceafe37b6bb66205a813"

def patch(source: Path):
    path = source / "session_phase.py"
    data = path.read_bytes()
    if hashlib.sha256(data).hexdigest() != EXPECTED_SHA256:
        raise RuntimeError("Unexpected upstream session_phase.py; inspect before patching")
    text = data.decode()
    anchors = [
        ("                    # OAuth init: GET authorize MIMIC top-level navigation của browser", "                    from login_guard import validate_authorize_url, validate_oauth_response\n                    validate_authorize_url(au)\n                    # OAuth init: GET authorize MIMIC top-level navigation của browser"),
        ('                    land = str(getattr(r, "url", "") or au)', '                    validate_oauth_response(r)\n                    land = str(getattr(r, "url", "") or au)'),
    ]
    for old, new in anchors:
        if text.count(old) != 1:
            raise RuntimeError("Upstream bootstrap anchor mismatch")
        text = text.replace(old, new, 1)
    start = text.index('async def _get_session_browser(')
    end = text.index('\nasync def get_session(', start)
    browser = text[start:end]
    browser_anchors = [
        ('    settings = load_settings()', '    from browser_login import load_browser_settings, is_workspace_page, select_personal_workspace, raise_login_failure, submit_totp_with_retry\n    settings = load_browser_settings()'),
        ('            code = generate_code(secret)', '            # Generate after the OTP input is ready; never retain an aging code.'),
        ('    engine_order = _browser_launch_order(settings.browser_engine)', '    engine_order = ("camoufox",)'),
        ('        from browser_phase import _navigate_to_authorize', '        from browser_login import navigate_to_authorize as _navigate_to_authorize'),
        ('        session_ready = False\n        while time.monotonic() < deadline:\n            cookies = await ctx.cookies("https://chatgpt.com/")',
         '        session_ready = False\n        workspace_selected = False\n        while time.monotonic() < deadline:\n            raise_login_failure()\n            if not workspace_selected and is_workspace_page(page.url):\n                workspace_selected = True\n                await select_personal_workspace(page)\n            cookies = await ctx.cookies("https://chatgpt.com/")'),
    ]
    old_submit = """            await _replace_input_text(otp_input, code, delay=60)
            log(f"[session] TOTP code entered")
            await asyncio.sleep(0.5)

            await _submit_auth_form(otp_input, (
                'button[type="submit"]',
                'button:has-text("Continue")',
                'button:has-text("Verify")',
            ), step="MFA")

            await asyncio.sleep(3.0)"""
    browser_anchors.append((old_submit, '            await submit_totp_with_retry(page, otp_input, secret, _replace_input_text, _submit_auth_form)'))
    for old, new in browser_anchors:
        if browser.count(old) != 1:
            raise RuntimeError('Upstream browser anchor mismatch')
        browser = browser.replace(old, new, 1)
    text = text[:start] + browser + text[end:]
    compile(text, str(path), "exec")
    path.write_text(text)

if __name__ == "__main__":
    patch(Path(sys.argv[1]))
