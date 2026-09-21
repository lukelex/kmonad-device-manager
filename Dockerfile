FROM golang:1.23-bookworm

ENV CGO_ENABLED=0 \
    GOFLAGS=-trimpath

RUN apt-get update \
    && apt-get install --no-install-recommends --yes systemd \
    && rm -rf /var/lib/apt/lists/*

WORKDIR /workspace

COPY go.mod ./
RUN go mod download

COPY . .

CMD ["go", "test", "./..."]
