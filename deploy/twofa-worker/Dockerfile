FROM node:24-bookworm-slim AS node
FROM python:3.12-slim-bookworm

# The core engine uses HTTP login and Node; the desktop/browser UI is unused.
COPY --from=node /usr/local/bin/node /usr/local/bin/node
RUN apt-get update && apt-get install -y --no-install-recommends ca-certificates git libstdc++6 libatomic1 \
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
COPY requirements.txt requirements-bridge.txt ./
RUN pip install --no-cache-dir -r requirements.txt \
    && useradd --uid 10001 --create-home worker \
    && install -d -o worker -g worker -m 700 /data
COPY worker.py login_guard.py rotation_service.py patch_upstream.py ./
RUN python patch_upstream.py /opt/change2fa
ENV PYTHONDONTWRITEBYTECODE=1 PYTHONUNBUFFERED=1 \
    CHANGE2FA_SOURCE_DIR=/opt/change2fa TWOFA_DATA_DIR=/data \
    PYTHONPATH=/opt/change2fa
# Resolve lazy HTTP login/MFA dependencies at build time without logging in.
RUN python -c "from service import TwoFAService; TwoFAService._resolve_dependencies(); import request_phase" \
    && node --version
USER 10001:10001
EXPOSE 8080
HEALTHCHECK --interval=30s --timeout=3s --start-period=20s \
  CMD python -c "import urllib.request; urllib.request.urlopen('http://127.0.0.1:8080/health', timeout=2)"
CMD ["python", "-m", "uvicorn", "worker:app", "--host", "0.0.0.0", "--port", "8080", "--workers", "1", "--no-access-log", "--log-level", "critical"]
