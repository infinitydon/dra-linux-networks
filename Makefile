IMAGE ?= ghcr.io/infinitydon/dra-linux-networks:0.3.1
HOST_DEVICE_NODE ?= ebpf-bng-node-02
STATIC_ADDRESS ?= 192.168.88.10/24
GATEWAY ?= 192.168.88.1

.PHONY: test test-e2e-multi-node test-e2e-host-device test-e2e-dpdk build image

test:
	go test ./...

test-e2e-multi-node:
	go test -tags=e2e ./tests/e2e -v -args -static-node "$(STATIC_NODE)" -dynamic-node "$(DYNAMIC_NODE)" -static-address "$(STATIC_ADDRESS)" -gateway "$(GATEWAY)"

test-e2e-host-device:
	go test -tags=e2e ./tests/e2e -v -args -host-device-node "$(HOST_DEVICE_NODE)"

test-e2e-dpdk:
	go test -tags=e2e ./tests/e2e -v -args -dpdk-node "$(DPDK_NODE)"

build:
	go build ./cmd/linux-net-dra

image:
	docker build -t $(IMAGE) .
