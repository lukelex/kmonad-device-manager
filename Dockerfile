FROM golang:1.27-bookworm

ENV CGO_ENABLED=0 \
    GOFLAGS=-trimpath

RUN apt-get update \
    && apt-get install --no-install-recommends --yes shellcheck systemd \
    && rm -rf /var/lib/apt/lists/*

WORKDIR /workspace

COPY go.mod ./
RUN go mod download

COPY . .

CMD ["go", "test", "./..."]
