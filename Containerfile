FROM golang:1.27 AS build
WORKDIR /src
COPY go.mod go.sum Makefile ./
RUN go mod download
COPY . .
RUN make build

FROM debian:bookworm-slim
RUN apt-get update && apt-get install -y --no-install-recommends ca-certificates curl \
 && rm -rf /var/lib/apt/lists/*
COPY --from=build /src/bin/switchboard /usr/local/bin/switchboard
COPY --from=build /src/bin/switchboard-admin /usr/local/bin/switchboard-admin
COPY --from=build /src/bin/switchboard-org /usr/local/bin/switchboard-org

ENV SWITCHBOARD_CONFIG=/etc/switchboard/config.json \
    SWITCHBOARD_GLOBAL_DB=/data/global.db

RUN mkdir -p /etc/switchboard /data

EXPOSE 8080

HEALTHCHECK --interval=30s --timeout=3s --start-period=5s --retries=3 \
    CMD curl -sf http://127.0.0.1:8080/healthz || exit 1

CMD ["switchboard", "-config", "/etc/switchboard/config.json"]
