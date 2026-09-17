GO ?= go
GOVULNCHECK_VERSION := v1.7.0

.PHONY: ci fmt fmt-check vet test build vuln

ci: fmt-check vet test vuln

fmt:
	$(GO) fmt ./...

fmt-check:
	@test -z "$$(gofmt -l cmd internal)" || { gofmt -l cmd internal; exit 1; }

vet:
	$(GO) vet ./...

test:
	$(GO) test -race ./...

build:
	mkdir -p dist
	CGO_ENABLED=0 GOOS=linux GOARCH=arm64 $(GO) build -tags lambda.norpc -trimpath -ldflags='-s -w' -o dist/bootstrap ./cmd/lambda
	rm -f dist/bootstrap.zip
	cd dist && zip -q -X bootstrap.zip bootstrap

vuln: build
	$(GO) run golang.org/x/vuln/cmd/govulncheck@$(GOVULNCHECK_VERSION) -mode=binary dist/bootstrap
