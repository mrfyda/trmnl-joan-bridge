.PHONY: build build-arm run

GO_IMAGE := golang:1.22-alpine

# Build for this machine (dev/test)
build:
	container run --rm \
	  --mount type=bind,source=$(CURDIR),target=/src -w /src \
	  -e CGO_ENABLED=0 \
	  $(GO_IMAGE) go build -o bin/joan-shim-local .

# Build ARM64 static binary for Raspberry Pi deployment
build-arm:
	container run --rm \
	  --mount type=bind,source=$(CURDIR),target=/src -w /src \
	  -e CGO_ENABLED=0 -e GOOS=linux -e GOARCH=arm64 \
	  $(GO_IMAGE) go build -o bin/joan-shim .

# Run via TRMNL Terminus (set TRMNL_SERVER, DEVICE_ID, ACCESS_TOKEN)
run: build
	container run --rm --name joan-shim \
	  --mount type=bind,source=$(CURDIR),target=/app -w /app \
	  -p 11112:11112 \
	  $(GO_IMAGE) ./bin/joan-shim-local \
	    -trmnl-server "$(TRMNL_SERVER)" \
	    -device-id "$(DEVICE_ID)" \
	    -access-token "$(ACCESS_TOKEN)" \
	    -refresh "$(or $(REFRESH),60s)"

