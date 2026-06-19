FROM golang:1.24-bookworm AS build
WORKDIR /src
COPY go.mod go.sum* ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o /out/linux-net-dra ./cmd/linux-net-dra

FROM debian:bookworm-slim
RUN apt-get update && apt-get install -y --no-install-recommends ca-certificates iproute2 && rm -rf /var/lib/apt/lists/*
COPY --from=build /out/linux-net-dra /usr/local/bin/linux-net-dra
ENTRYPOINT ["/usr/local/bin/linux-net-dra"]
