.PHONY: build test vet lint e2e playwright clean

GO      ?= go
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -X main.version=$(VERSION)

build:
	mkdir -p bin
	$(GO) build -ldflags "$(LDFLAGS)" -o bin/pelicula ./cmd/pelicula
	$(GO) build -ldflags "$(LDFLAGS)" -o bin/pelicula-server ./cmd/pelicula-server

test:
	$(GO) test -race ./...

vet:
	$(GO) vet ./...
	@out="$$(gofmt -l .)"; \
	if [ -n "$$out" ]; then \
		echo "gofmt needed on:"; echo "$$out"; exit 1; \
	fi

lint:
	$(GO) run honnef.co/go/tools/cmd/staticcheck@latest ./...

e2e:
	$(GO) test -tags integration -count=1 -timeout 20m ./tests/integration/...

playwright:
	cd tests/playwright && npm test

clean:
	rm -rf bin tests/playwright/report tests/playwright/test-results
