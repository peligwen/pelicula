.PHONY: build test test-procula test-middleware test-cli test-race test-cover e2e install-hooks check-hooks verify

build:
	cd cmd/pelicula && go build -ldflags "-X main.version=$$(git describe --tags --always --dirty 2>/dev/null || echo dev)" -o ../../bin/pelicula .

check-hooks:
	@if [ "$$(git config core.hooksPath)" != ".githooks" ]; then \
		echo "hooks not installed; running make install-hooks..."; \
		$(MAKE) install-hooks; \
	fi

test: check-hooks test-procula test-middleware test-cli

test-procula:
	cd procula && go test -race -v ./...

test-middleware:
	cd middleware && go test -race -v ./...

test-cli:
	cd cmd/pelicula && go test -race -v ./...

test-race:
	cd procula && go test -race -v ./...
	cd middleware && go test -race -v ./...
	cd cmd/pelicula && go test -race -v ./...

test-cover:
	cd procula && go test -coverprofile=coverage.out ./... && go tool cover -func=coverage.out
	cd middleware && go test -coverprofile=coverage.out ./... && go tool cover -func=coverage.out

e2e:
	bash tests/e2e.sh

install-hooks:
	git config core.hooksPath .githooks
	git config merge.ff false
	@echo "hooks installed — pre-commit, pre-push, pre-merge-commit active"
	@echo "merges into main will run the full suite (unit + e2e, ~10 min)"
	@echo "bypass any hook with --no-verify"

verify: test e2e
