IMAGE ?= ghcr.io/infinitydon/dra-linux-networks:0.1.0

.PHONY: test build image

test:
	go test ./...

build:
	go build ./cmd/linux-net-dra

image:
	docker build -t $(IMAGE) .
