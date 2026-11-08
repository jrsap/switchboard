FROM python:3.12-slim

ENV PYTHONUNBUFFERED=1 \
    SWITCHBOARD_HOST=0.0.0.0 \
    SWITCHBOARD_PORT=8000 \
    SWITCHBOARD_CONFIG_PATH=/app/config.yaml \
    SWITCHBOARD_DATABASE_URL=sqlite:////data/switchboard.db

WORKDIR /app

COPY requirements.txt .
RUN pip install --no-cache-dir -r requirements.txt

COPY switchboard/ switchboard/
COPY config.example.yaml config.yaml

RUN mkdir -p /data

EXPOSE 8000

HEALTHCHECK --interval=30s --timeout=3s --start-period=5s --retries=3 \
    CMD python -c "import urllib.request; urllib.request.urlopen('http://127.0.0.1:8000/healthz', timeout=2)" || exit 1

CMD ["python", "-m", "uvicorn", "switchboard.main:app", "--host", "0.0.0.0", "--port", "8000"]
