# syntax=docker/dockerfile:1

# ── build stage ──────────────────────────────────────────────────────────────
FROM golang:1.26-alpine AS build

WORKDIR /src

COPY go.mod go.sum ./
RUN go mod download

COPY *.go ./
COPY pv3/ ./pv3/

RUN CGO_ENABLED=0 go build -trimpath -o /trmnl-joan-bridge -ldflags="-s -w" .

# ── runtime stage ─────────────────────────────────────────────────────────────
# scratch + CA certs = smallest possible image that can make HTTPS requests.
FROM scratch

COPY --from=build /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/
COPY --from=build /trmnl-joan-bridge /trmnl-joan-bridge

EXPOSE 11112

# Required: TRMNL_SERVER, DEVICE_ID, ACCESS_TOKEN
# Optional: REFRESH_INTERVAL (default 60s), LISTEN_ADDR (default :11112)
ENTRYPOINT ["/trmnl-joan-bridge"]
