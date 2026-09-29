"""Apply two checked bootstrap guards to the fixed upstream login implementation."""
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
    compile(text, str(path), "exec")
    path.write_text(text)

if __name__ == "__main__":
    patch(Path(sys.argv[1]))
