.PHONY: build test fmt vet

build:
	go build -o MailSalonSync ./cmd/MailSalonSync

test:
	go test ./...

fmt:
	gofmt -w ./cmd ./internal

vet:
	go vet ./...
