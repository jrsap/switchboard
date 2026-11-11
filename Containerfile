FROM golang:1.27 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -o /out/switchboard ./cmd/switchboard \
 && CGO_ENABLED=0 go build -o /out/switchboard-admin ./cmd/switchboard-admin

FROM debian:bookworm-slim
RUN apt-get update && apt-get install -y --no-install-recommends ca-certificates curl \
 && rm -rf /var/lib/apt/lists/*
COPY --from=build /out/switchboard /usr/local/bin/switchboard
COPY --from=build /out/switchboard-admin /usr/local/bin/switchboard-admin

ENV SWITCHBOARD_CONFIG=/etc/switchboard/config.json \
    SWITCHBOARD_GLOBAL_DB=/data/global.db

RUN mkdir -p /etc/switchboard /data

EXPOSE 8080

HEALTHCHECK --interval=30s --timeout=3s --start-period=5s --retries=3 \
    CMD curl -sf http://127.0.0.1:8080/healthz || exit 1

CMD ["switchboard", "-config", "/etc/switchboard/config.json"]
