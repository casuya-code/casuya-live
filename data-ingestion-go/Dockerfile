FROM golang:1.26-alpine AS builder
WORKDIR /build
COPY go.mod go.sum* ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -ldflags="-s -w" -o /out/ingestor ./cmd/ingestor

FROM alpine:3.20
RUN adduser -D -H -u 1000 casuya
USER casuya
COPY --from=builder /out/ingestor /usr/local/bin/ingestor
ENTRYPOINT ["ingestor"]