GO ?= go
BIN ?= micam

# Registry coordinates for the image targets. Override on the command line:
#   make docker-push IMAGE=youruser/micam TAG=0.4.0
IMAGE ?= hoshinosuzumi/micam-go
TAG ?= dev

.PHONY: fmt vet test build cross docker docker-push docker-push-multi fpk clean

fmt:
	gofmt -w .

vet:
	$(GO) vet ./...

test:
	$(GO) test -count=1 ./...

build:
	mkdir -p bin
	CGO_ENABLED=0 $(GO) build -trimpath -ldflags="-s -w" -o bin/$(BIN) ./cmd/micam

cross:
	mkdir -p bin
	CGO_ENABLED=0 GOOS=linux GOARCH=arm64 $(GO) build -trimpath -ldflags="-s -w" \
		-o bin/$(BIN)-linux-arm64 ./cmd/micam

# Build for the machine running the command.
docker:
	docker buildx build --platform linux/arm64 -t $(IMAGE):$(TAG) .

# Build and push one architecture. Run this on arm64 hardware: the Go toolchain
# then runs natively instead of under emulation.
docker-push:
	docker buildx build --platform linux/arm64 \
		-t $(IMAGE):$(TAG) -t $(IMAGE):latest --push .

# Both architectures in one manifest. The foreign platform is compiled under
# QEMU, which is noticeably slower.
docker-push-multi:
	docker buildx build --platform linux/amd64,linux/arm64 \
		-t $(IMAGE):$(TAG) -t $(IMAGE):latest --push .

# Build the fnOS application package. Needs fnpack:
# https://developer.fnnas.com/docs/cli/fnpack
fpk:
	@command -v fnpack >/dev/null 2>&1 || { \
		echo "fnpack not found - download it from https://developer.fnnas.com/docs/cli/fnpack"; \
		exit 1; }
	fnpack build --directory packaging/fnos

clean:
	rm -rf bin
