.PHONY: tidy build vet test check fmt silt

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
