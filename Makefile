GO ?= go
BIN ?= micam

.PHONY: fmt vet test build cross docker clean

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

docker:
	docker buildx build --platform linux/arm64 -t micam:arm64 .

clean:
	rm -rf bin
