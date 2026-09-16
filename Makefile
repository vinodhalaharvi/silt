.PHONY: tidy build vet test check fmt silt ci

silt:
	go build -o bin/silt ./cmd/silt

tidy:
	go mod tidy

build:
	go build ./...

vet:
	go vet ./...

test:
	go test ./...

fmt:
	gofmt -l -w .

check: vet test
	@gofmt -l . | grep . && { echo "unformatted files above"; exit 1; } || echo "gofmt clean"

# The same checks CI runs. BUILDROOT must be a checkout of the pinned release.
ci: check silt
	@test -n "$(BUILDROOT)" || { echo "set BUILDROOT=<buildroot 2025.02.16 checkout>"; exit 1; }
	SILT_BUILDROOT="$(BUILDROOT)" go test ./...
	ci/check-images.sh "$(BUILDROOT)" bin/silt
