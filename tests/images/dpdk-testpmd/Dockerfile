FROM ubuntu:24.04

RUN apt-get update && \
    apt-get install -y --no-install-recommends dpdk pciutils ca-certificates && \
    rm -rf /var/lib/apt/lists/*

COPY run-testpmd.sh /usr/local/bin/run-testpmd
RUN chmod 0755 /usr/local/bin/run-testpmd

ENTRYPOINT ["/usr/local/bin/run-testpmd"]
