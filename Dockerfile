# ---- build ----
FROM golang:1.27-trixie AS build
WORKDIR /src
ARG VERSION=dev
COPY . .
# go mod tidy fills go.sum from the official Go proxy (checked against sum.golang.org).
RUN go mod tidy && go vet ./... && go test ./... \
 && CGO_ENABLED=1 go build -trimpath -ldflags "-s -w -X main.version=${VERSION}" -o /out/wa-gateway .

# ---- run ----
FROM debian:trixie-slim
RUN apt-get update && apt-get install -y --no-install-recommends ca-certificates tzdata wget \
 && rm -rf /var/lib/apt/lists/* \
 && useradd -r -u 10001 -m gw && mkdir -p /data && chown gw:gw /data
COPY --from=build /out/wa-gateway /usr/local/bin/wa-gateway
USER gw
ENV DATA_DIR=/data PORT=8080 TZ=Asia/Kolkata
VOLUME /data
EXPOSE 8080
HEALTHCHECK --interval=30s --timeout=5s --retries=3 CMD wget -qO- http://127.0.0.1:8080/health >/dev/null || exit 1
ENTRYPOINT ["/usr/local/bin/wa-gateway"]
