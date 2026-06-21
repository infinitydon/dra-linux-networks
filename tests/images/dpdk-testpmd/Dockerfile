FROM ubuntu:24.04 AS build

RUN apt-get update && \
    apt-get install -y --no-install-recommends \
      ca-certificates git build-essential meson ninja-build pkg-config \
      python3-pyelftools libnuma-dev libpcap-dev libelf-dev && \
    rm -rf /var/lib/apt/lists/*

ARG DPDK_VERSION=v26.03
RUN git clone --branch "${DPDK_VERSION}" --depth 1 https://github.com/DPDK/dpdk.git /src/dpdk && \
    meson setup /src/dpdk/build /src/dpdk \
      -Dplatform=generic \
      -Ddefault_library=static \
      -Dtests=false \
      -Dexamples= && \
    ninja -C /src/dpdk/build app/dpdk-testpmd && \
    cp /src/dpdk/build/app/dpdk-testpmd /usr/local/bin/dpdk-testpmd

FROM ubuntu:24.04

RUN apt-get update && \
    apt-get install -y --no-install-recommends \
      ca-certificates pciutils libnuma1 libpcap0.8 libelf1 libatomic1 \
      libibverbs1 libmlx5-1 libmlx4-1 libmana1 && \
    rm -rf /var/lib/apt/lists/*

COPY --from=build /usr/local/bin/dpdk-testpmd /usr/local/bin/dpdk-testpmd
COPY run-testpmd.sh /usr/local/bin/run-testpmd
RUN chmod 0755 /usr/local/bin/run-testpmd

ENTRYPOINT ["/usr/local/bin/run-testpmd"]
