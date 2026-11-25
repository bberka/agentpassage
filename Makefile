BINARY := apx
LDFLAGS := -s -w
BUILD_FLAGS := -trimpath -buildvcs=false
INSTALL_DIR ?= $(HOME)/.local/bin

.PHONY: build install test clean size cross bench

build:
	CGO_ENABLED=0 go build $(BUILD_FLAGS) -ldflags="$(LDFLAGS)" -o $(BINARY) ./cmd/apx

install: build
	mkdir -p $(INSTALL_DIR)
	install -m 0755 $(BINARY) $(INSTALL_DIR)/$(BINARY)
	@echo "Installed $(BINARY) -> $(INSTALL_DIR)/$(BINARY)"

test:
	go test -race ./...

bench:
	go test -bench=. -benchmem ./internal/privacy ./internal/router ./internal/usage

size: build
	@ls -lh $(BINARY)

cross:
	mkdir -p dist
	CGO_ENABLED=0 GOOS=linux   GOARCH=amd64 go build $(BUILD_FLAGS) -ldflags="$(LDFLAGS)" -o dist/$(BINARY)-linux-amd64   ./cmd/apx
	CGO_ENABLED=0 GOOS=linux   GOARCH=arm64 go build $(BUILD_FLAGS) -ldflags="$(LDFLAGS)" -o dist/$(BINARY)-linux-arm64   ./cmd/apx
	CGO_ENABLED=0 GOOS=darwin  GOARCH=amd64 go build $(BUILD_FLAGS) -ldflags="$(LDFLAGS)" -o dist/$(BINARY)-darwin-amd64  ./cmd/apx
	CGO_ENABLED=0 GOOS=darwin  GOARCH=arm64 go build $(BUILD_FLAGS) -ldflags="$(LDFLAGS)" -o dist/$(BINARY)-darwin-arm64  ./cmd/apx
	CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build $(BUILD_FLAGS) -ldflags="$(LDFLAGS)" -o dist/$(BINARY)-windows-amd64.exe ./cmd/apx
	CGO_ENABLED=0 GOOS=windows GOARCH=arm64 go build $(BUILD_FLAGS) -ldflags="$(LDFLAGS)" -o dist/$(BINARY)-windows-arm64.exe ./cmd/apx

clean:
	rm -rf $(BINARY) dist
