BINARY := cloudflare-exporter
IMAGE  := cloudflare-prometheus-exporter-go

CHART  := charts/cloudflare-exporter/Chart.yaml

.PHONY: build test lint fmt container bump clean

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

# Sets both chart fields to VERSION (no leading v): appVersion is the image the
# chart deploys, and the chart version tracks it so Helm never sees a reused
# version. Commit, then tag v$(VERSION) — release.yml refuses a mismatch.
bump:
	@test -n "$(VERSION)" || { echo "usage: make bump VERSION=x.y.z" >&2; exit 1; }
	@echo "$(VERSION)" | grep -Eq '^[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.-]+)?$$' || { echo "VERSION must be semver without leading v" >&2; exit 1; }
	sed -i -E 's/^version: .*/version: $(VERSION)/; s/^appVersion: .*/appVersion: "$(VERSION)"/' $(CHART)
	@grep -E '^(version|appVersion):' $(CHART)

clean:
	rm -rf bin
