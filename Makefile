BINARY := bin/mirador
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -s -w -X github.com/miradorlabs/mirador-cli/cmd.Version=$(VERSION)

.PHONY: build
build:
	go build -ldflags "$(LDFLAGS)" -o $(BINARY) .

# Install onto PATH as `mirador`. Plain `go install` would name it `mirador-cli`
# after the module path, so the binary is placed explicitly.
.PHONY: install
install:
	go build -ldflags "$(LDFLAGS)" -o "$(shell go env GOPATH)/bin/mirador" .
	@echo "installed $(shell go env GOPATH)/bin/mirador"
	@command -v mirador >/dev/null 2>&1 || echo "note: $(shell go env GOPATH)/bin is not on your PATH"

.PHONY: test
test:
	go test ./...

.PHONY: cover
cover:
	go test -cover ./...

.PHONY: fmt
fmt:
	gofmt -w .

.PHONY: vet
vet:
	go vet ./...

.PHONY: check
check: fmt vet test

# Build the release archives locally without publishing — same path CI takes.
.PHONY: release-dry-run
release-dry-run:
	go run github.com/goreleaser/goreleaser/v2@latest release --snapshot --clean --skip=publish

.PHONY: clean
clean:
	rm -rf bin dist

# Cross-compiled release binaries. CGO is off so each one is a static binary that
# runs on any machine of its platform without a matching libc.
.PHONY: dist
dist:
	@mkdir -p dist
	@for target in darwin/amd64 darwin/arm64 linux/amd64 linux/arm64 windows/amd64; do \
		os=$${target%/*}; arch=$${target#*/}; ext=""; \
		if [ "$$os" = "windows" ]; then ext=".exe"; fi; \
		echo "building dist/mirador-$$os-$$arch$$ext"; \
		CGO_ENABLED=0 GOOS=$$os GOARCH=$$arch go build -ldflags "$(LDFLAGS)" \
			-o dist/mirador-$$os-$$arch$$ext . || exit 1; \
	done
