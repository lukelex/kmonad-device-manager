FROM --platform=$BUILDPLATFORM golang:1.27-bookworm AS builder

ARG TARGETOS
ARG TARGETARCH
ARG VERSION=dev

ENV CGO_ENABLED=0 \
    GOFLAGS=-trimpath

WORKDIR /src

COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN GOOS="$TARGETOS" GOARCH="$TARGETARCH" \
    go build -buildvcs=false -trimpath \
    -ldflags="-s -w -X main.version=$VERSION" \
    -o /out/kmonad-device-manager ./cmd/kmonad-device-manager

FROM golang:1.27-bookworm AS development

RUN apt-get update \
    && apt-get install --no-install-recommends --yes shellcheck systemd \
    && rm -rf /var/lib/apt/lists/*

WORKDIR /workspace

COPY go.mod go.sum ./
RUN go mod download

COPY . .

CMD ["go", "test", "./..."]

FROM scratch AS release

COPY --from=builder /out/kmonad-device-manager /kmonad-device-manager
