.PHONY: test test-procula test-middleware test-race test-cover

test: test-procula test-middleware

test-procula:
	cd procula && go test -v ./...

test-middleware:
	cd middleware && go test -v ./...

test-race:
	cd procula && go test -race -v ./...
	cd middleware && go test -race -v ./...

test-cover:
	cd procula && go test -coverprofile=coverage.out ./... && go tool cover -func=coverage.out
	cd middleware && go test -coverprofile=coverage.out ./... && go tool cover -func=coverage.out
