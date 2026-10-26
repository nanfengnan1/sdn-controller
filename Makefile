GO ?= go

.PHONY: build test vet
build:
	mkdir -p bin
	CGO_ENABLED=0 $(GO) build -o bin/sdn-controller ./cmd/sdn-controller
	CGO_ENABLED=0 $(GO) build -o bin/sdnc ./cmd/sdnc
test:
	$(GO) test -race ./...
vet:
	$(GO) vet ./...
