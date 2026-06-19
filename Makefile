IMAGE ?= ghcr.io/infinitydon/dra-linux-networks:0.1.10
STATIC_ADDRESS ?= 192.168.88.10/24
GATEWAY ?= 192.168.88.1

.PHONY: test test-e2e-multi-node build image

test:
	go test ./...

test-e2e-multi-node:
	go test -tags=e2e ./tests/e2e -v -args -static-node "$(STATIC_NODE)" -dynamic-node "$(DYNAMIC_NODE)" -static-address "$(STATIC_ADDRESS)" -gateway "$(GATEWAY)"

build:
	go build ./cmd/linux-net-dra

image:
	docker build -t $(IMAGE) .
