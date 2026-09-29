FROM node:24-bookworm-slim AS node
FROM python:3.12-slim-bookworm

# Keep the desktop server disabled; the private bridge owns browser lifecycle.
COPY --from=node /usr/local/bin/node /usr/local/bin/node
RUN apt-get update && apt-get install -y --no-install-recommends ca-certificates git libstdc++6 libatomic1 libgtk-3-0 libdbus-glib-1-2 libxt6 libasound2 \
    && rm -rf /var/lib/apt/lists/*
WORKDIR /opt/change2fa
RUN git init . \
    && git remote add origin https://github.com/zixfelw/Change-2fa-Password-GPT-Auto.git \
    && git fetch --depth 1 origin 9fa8b481a2e5252ce2782833f6d1e0623c68fdce \
    && git checkout --detach FETCH_HEAD \
    && test "$(git rev-parse HEAD)" = 9fa8b481a2e5252ce2782833f6d1e0623c68fdce \
    && rm -rf .git static demo.mp4 \
    && apt-get purge -y git && apt-get autoremove -y && rm -rf /var/lib/apt/lists/*
WORKDIR /app
COPY requirements.txt requirements-bridge.txt requirements-browser.txt ./
ENV XDG_CACHE_HOME=/opt/camoufox-cache
RUN pip install --no-cache-dir -r requirements.txt -r requirements-browser.txt \
    && python -m camoufox fetch official/stable/152.0.4-beta.28 \
    && chmod -R a+rX /opt/camoufox-cache \
    && useradd --uid 10001 --create-home worker \
    && install -d -o worker -g worker -m 700 /data
RUN apt-get update && apt-get install -y --no-install-recommends libx11-xcb1 \
    && rm -rf /var/lib/apt/lists/* \
    && python -c "from pathlib import Path; import os, subprocess; p=next(Path('/opt/camoufox-cache').rglob('libxul.so')); r=subprocess.run(['ldd', str(p)], env=dict(os.environ, LD_LIBRARY_PATH=str(p.parent)), capture_output=True, text=True, check=True); assert 'not found' not in r.stdout, r.stdout"
COPY worker.py login_guard.py rotation_service.py patch_upstream.py browser_login.py ./
RUN python patch_upstream.py /opt/change2fa
ENV PYTHONDONTWRITEBYTECODE=1 PYTHONUNBUFFERED=1 \
    CHANGE2FA_SOURCE_DIR=/opt/change2fa TWOFA_DATA_DIR=/data \
    PYTHONPATH=/opt/change2fa TWOFA_LOGIN_ENGINE=camoufox
# Resolve lazy HTTP login/MFA dependencies at build time without logging in.
RUN python -c "from service import TwoFAService; TwoFAService._resolve_dependencies(); import request_phase" \
    && node --version
USER 10001:10001
EXPOSE 8080
HEALTHCHECK --interval=30s --timeout=3s --start-period=20s \
  CMD python -c "import urllib.request; urllib.request.urlopen('http://127.0.0.1:8080/health', timeout=2)"
CMD ["python", "-m", "uvicorn", "worker:app", "--host", "0.0.0.0", "--port", "8080", "--workers", "1", "--no-access-log", "--log-level", "critical"]
