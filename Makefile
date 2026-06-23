BINARY   := monitor-network-rtmp
PKG      := github.com/vietanha34/monitor-network-rtmp
VERSION  := $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS  := -X main.version=$(VERSION)

.PHONY: vet build build-linux clean run

## Run static analysis.
vet:
	go vet ./...

## Build the exporter for the current host.
build:
	go build -ldflags "$(LDFLAGS)" -o bin/$(BINARY) .

## Cross-compile static Linux binaries (amd64 + arm64) for Ubuntu deployment.
build-linux:
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -ldflags "$(LDFLAGS)" -o bin/$(BINARY)-linux-amd64 .
	CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -ldflags "$(LDFLAGS)" -o bin/$(BINARY)-linux-arm64 .

## Run locally on port :9101, monitoring port 1935.
run:
	go run -ldflags "$(LDFLAGS)" . --target-port 1935 --listen-address :9101

clean:
	rm -rf bin
