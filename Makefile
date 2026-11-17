GO ?= go
BIN ?= micam

# 版本号来自 git tag（发布时来自 MICAM_VERSION），编译期注入，
# 所以二进制里的版本永远对得上当时打的那个 tag。
VERSION ?= $(shell ./scripts/version.sh)
LDFLAGS := -s -w -X 'micam/internal/cli.Version=$(VERSION)'

# Registry coordinates for the image targets. Override on the command line:
#   make docker-push IMAGE=youruser/micam TAG=0.4.0
IMAGE ?= hoshinosuzumi/micam-go
TAG ?= dev

.PHONY: fmt vet test version build cross web docker docker-push docker-push-multi fpk clean

fmt:
	gofmt -w .

vet:
	$(GO) vet ./...

test:
	$(GO) test -count=1 ./...

version:
	@echo $(VERSION)

build:
	mkdir -p bin
	CGO_ENABLED=0 $(GO) build -trimpath -ldflags="$(LDFLAGS)" -o bin/$(BIN) ./cmd/micam

cross:
	mkdir -p bin
	CGO_ENABLED=0 GOOS=linux GOARCH=arm64 $(GO) build -trimpath -ldflags="$(LDFLAGS)" \
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

# Build the management UI. It is a build product, so it never lives in the tree.
web:
	@test -d web/node_modules || (cd web && npm ci --no-audit --no-fund)
	cd web && npm run build

# Build the fnOS application package(s). Needs fnpack:
# https://developer.fnnas.com/docs/cli/fnpack
#
# 服务是静态二进制，所以每种架构一个包：make fpk ARCHS=amd64 只出 x86 的。
ARCHS ?= amd64 arm64
fpk: web
	@command -v fnpack >/dev/null 2>&1 || { \
		echo "fnpack not found - download it from https://developer.fnnas.com/docs/cli/fnpack"; \
		exit 1; }
	@for arch in $(ARCHS); do ./packaging/build-fpk.sh $$arch || exit 1; done

clean:
	rm -rf bin .tmp/fpk-* web/dist
