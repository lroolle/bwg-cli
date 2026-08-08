BINARY := bwg
VERSION := $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
COMMIT  := $(shell git rev-parse --short HEAD 2>/dev/null || echo none)
LDFLAGS := -ldflags "-s -w -X main.version=$(VERSION)"

.PHONY: build
build:
	go build $(LDFLAGS) -o $(BINARY) ./cmd/bwg

.PHONY: test
test:
	go test ./...

.PHONY: test-race
test-race:
	go test -race ./...

.PHONY: cover
cover:
	go test ./... -coverprofile=cover.out
	go tool cover -func=cover.out | tail -1

.PHONY: vet
vet:
	go vet ./...

.PHONY: fmt
fmt:
	gofmt -l -w .

.PHONY: lint
lint:
	@gofmt -l . | grep -v '^$$' && { echo "gofmt needed on the files above"; exit 1; } || echo "gofmt clean"

# check is what CI runs and what a commit should pass.
.PHONY: check
check: lint vet test-race build
	@echo "all checks passed"

.PHONY: install
install: build
	@mkdir -p $${GOBIN:-$${GOPATH:-$$HOME/go}/bin}
	@cp $(BINARY) $${GOBIN:-$${GOPATH:-$$HOME/go}/bin}/$(BINARY)
	@echo "installed $(BINARY) to $${GOBIN:-$${GOPATH:-$$HOME/go}/bin}"

.PHONY: cross
cross:
	@mkdir -p dist
	GOOS=linux   GOARCH=amd64 go build $(LDFLAGS) -o dist/$(BINARY)_linux_x86_64   ./cmd/bwg
	GOOS=linux   GOARCH=arm64 go build $(LDFLAGS) -o dist/$(BINARY)_linux_arm64    ./cmd/bwg
	GOOS=darwin  GOARCH=amd64 go build $(LDFLAGS) -o dist/$(BINARY)_macOS_x86_64   ./cmd/bwg
	GOOS=darwin  GOARCH=arm64 go build $(LDFLAGS) -o dist/$(BINARY)_macOS_arm64    ./cmd/bwg
	GOOS=windows GOARCH=amd64 go build $(LDFLAGS) -o dist/$(BINARY)_windows_x86_64.exe ./cmd/bwg

.PHONY: completions
completions: build
	@mkdir -p completions
	./$(BINARY) completion bash > completions/$(BINARY).bash
	./$(BINARY) completion zsh  > completions/_$(BINARY)
	./$(BINARY) completion fish > completions/$(BINARY).fish

.PHONY: clean
clean:
	rm -rf $(BINARY) dist completions cover.out

.DEFAULT_GOAL := build
