FROM golang:1.22-alpine AS build
WORKDIR /src

COPY go.mod ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o /out/collector ./cmd/collector

FROM alpine:3.20
WORKDIR /app
COPY --from=build /out/collector /usr/local/bin/collector

ENTRYPOINT ["/usr/local/bin/collector"]

