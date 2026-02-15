FROM golang:1.22-alpine AS builder

WORKDIR /src

COPY go.mod go.sum ./
RUN go mod download

COPY . .
ARG TARGETOS=linux
ARG TARGETARCH
RUN CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH:-amd64} go build -o /out/ingest ./cmd/ingest

FROM alpine:3.20

WORKDIR /app
RUN adduser -D -u 10001 app

COPY --from=builder /out/ingest /app/ingest

USER app

ENTRYPOINT ["/app/ingest"]

