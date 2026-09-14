.PHONY: tidy build vet test check

tidy:
	go mod tidy

build:
	go build ./...

vet:
	go vet ./...

test:
	go test ./...

check: vet test
