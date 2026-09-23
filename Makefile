BINARY := cloudflare-exporter
IMAGE  := cloudflare-prometheus-exporter-go

.PHONY: build test lint fmt container clean

build:
	CGO_ENABLED=0 go build -trimpath -o bin/$(BINARY) ./cmd/exporter

test:
	go test ./...

lint:
	test -z "$$(gofmt -l .)" || { gofmt -l .; exit 1; }
	go vet ./...
	golangci-lint run ./...

fmt:
	gofmt -w .

container:
	podman build -t $(IMAGE) -f Containerfile .

clean:
	rm -rf bin
