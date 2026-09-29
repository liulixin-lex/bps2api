"""Validate login bootstrap before sending credentials into the next state.

Never solve access challenges or retry denied sessions. Errors contain fixed
codes only, not response bodies, cookies, URLs, email, or submitted secrets.
"""
from urllib.parse import parse_qs, urlsplit


class LoginBootstrapError(RuntimeError):
    def __init__(self, code: str):
        self.code = code
        super().__init__(code)


def validate_authorize_url(url: str) -> None:
    try:
        parsed = urlsplit(url)
        valid = (
            parsed.scheme == "https" and parsed.hostname == "auth.openai.com"
            and parsed.port in (None, 443) and parsed.username is None
            and parsed.password is None and not parsed.fragment
            and parsed.path in {"/authorize", "/api/accounts/authorize"}
            and len(parse_qs(parsed.query).get("state", [])) == 1
            and bool(parse_qs(parsed.query)["state"][0].strip())
        )
    except (TypeError, ValueError, KeyError):
        valid = False
    if not valid:
        raise LoginBootstrapError("login_bootstrap_rejected")


def validate_oauth_response(response) -> None:
    status = response.status_code
    if status == 403:
        raise LoginBootstrapError("login_access_denied")
    if status == 429:
        raise LoginBootstrapError("login_rate_limited")
    if status != 200:
        raise LoginBootstrapError("login_bootstrap_rejected")
    if str(response.headers.get("cf-mitigated", "")).lower() == "challenge":
        raise LoginBootstrapError("login_interaction_required")
    try:
        parsed = urlsplit(str(response.url))
        valid = (parsed.scheme == "https" and parsed.hostname == "auth.openai.com"
                 and parsed.port in (None, 443) and parsed.username is None
                 and parsed.password is None)
    except (TypeError, ValueError):
        valid = False
    if not valid:
        raise LoginBootstrapError("login_bootstrap_rejected")
