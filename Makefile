.PHONY: build test unit conformance docker

build:
	go build -o bin/dzcollab ./cmd/dzcollab

unit:
	go vet ./...
	go test -race ./...

conformance: build
	cd conformance && npm ci --silent && DZCOLLAB_SERVER=$(CURDIR)/bin/dzcollab npm test

test: unit conformance

docker:
	docker build -t dzstudio-collab-server .
