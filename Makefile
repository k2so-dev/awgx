VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
RELEASE_KEY ?=
LDFLAGS := -s -w -X github.com/k2so-dev/awgx/internal/cli.Version=$(VERSION) -X github.com/k2so-dev/awgx/internal/upgrade.PublicKey=$(RELEASE_KEY)

.PHONY: build build-all licenses test lint clean web e2e

build:
	CGO_ENABLED=0 go build -trimpath -ldflags "$(LDFLAGS)" -o bin/awgx ./cmd/awgx

build-all:
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags "$(LDFLAGS)" -o dist/awgx-linux-amd64 ./cmd/awgx
	CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -trimpath -ldflags "$(LDFLAGS)" -o dist/awgx-linux-arm64 ./cmd/awgx

licenses:
	mkdir -p dist
	{ printf '== Go %s\n\n' "$$(go env GOVERSION)"; cat "$$(go env GOROOT)/LICENSE"; \
	go list -deps -f '{{with .Module}}{{if not .Main}}{{.Path}} {{.Version}} {{.Dir}}{{end}}{{end}}' ./cmd/awgx | sort -u | \
	while read path version dir; do printf '\n== %s %s\n\n' "$$path" "$$version"; \
	find "$$dir" -maxdepth 1 -type f \( -iname 'LICEN[CS]E*' -o -iname 'COPYING*' -o -iname 'NOTICE*' \) -exec cat {} +; done; \
	printf '\n'; cat web/LICENSES.txt; } > dist/THIRD_PARTY_LICENSES.txt

test:
	go test ./...

lint:
	go vet ./...

clean:
	rm -rf bin dist

web:
	cd web && npm ci && npm run build

e2e:
	test/e2e/run.sh $(HOST)
