BINARY := dist/mrdns
PKG := ./...

.PHONY: all build test race vet fmtcheck check run secrets install tidy clean

all: build

build:
	CGO_ENABLED=0 go build -trimpath -o $(BINARY) ./cmd/mrdns

test:
	go test $(PKG)

race:
	go test -race $(PKG)

vet:
	go vet $(PKG)

fmtcheck:
	@out=$$(gofmt -l .); if [ -n "$$out" ]; then echo "gofmt needed:"; echo "$$out"; exit 1; fi

check: fmtcheck vet test

# Run locally against configs/mrdns.dev.yaml (writes state under ./.dev).
run: build
	MRDNS_TOKEN=$${MRDNS_TOKEN:-dev-token} \
	MRDNS_COOKIE_KEY=$${MRDNS_COOKIE_KEY:-$$(printf '%064d' 1)} \
	$(BINARY) -config configs/mrdns.dev.yaml

# Print strong secrets for /opt/mrdns/etc/mrdns.env.
secrets:
	@echo "MRDNS_TOKEN=$$(openssl rand -hex 32)"
	@echo "MRDNS_COOKIE_KEY=$$(openssl rand -hex 32)"

install: build
	sudo ./deploy/install.sh

tidy:
	go mod tidy

clean:
	rm -rf dist .dev
