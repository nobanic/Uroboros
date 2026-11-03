FROM python:3.12-slim@sha256:a6e34c598f2467ed0e9a8d349809fcd8b5c603269512df273a0bb1784edc11b1
WORKDIR /opt/api-lab
COPY api_lab/requirements.lock /opt/api-lab/requirements.lock
RUN pip install --no-cache-dir -r requirements.lock
COPY api_lab/semgrep.lock /opt/api-lab/semgrep.lock
RUN python -m venv /opt/semgrep && /opt/semgrep/bin/pip install --no-cache-dir -r semgrep.lock
COPY arsenal/__init__.py arsenal/contracts.py arsenal/execmgr.py arsenal/auth.py arsenal/authmw.py arsenal/register.py /opt/api-lab/arsenal/
COPY api_lab /opt/api-lab/api_lab/
ENV PYTHONPATH=/opt/api-lab PYTHONUNBUFFERED=1 API_LAB_DB=/var/lib/api-lab/jobs.sqlite3 API_LAB_IDENTITIES=/run/api-lab/identities.json
ENV API_LAB_SOURCE_PROJECTS=/opt/api-lab/api_lab/source_projects.json API_LAB_SEMGREP=/opt/semgrep/bin/semgrep SEMGREP_SEND_METRICS=off SEMGREP_ENABLE_VERSION_CHECK=0
EXPOSE 8010
CMD ["python", "-m", "api_lab.server"]
