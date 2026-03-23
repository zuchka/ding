# Build stage
FROM golang:1.22-alpine AS builder
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -ldflags="-s -w" -o /ding ./cmd/ding/

# Final stage — scratch for minimal image
FROM scratch
LABEL org.opencontainers.image.title="ding"
LABEL org.opencontainers.image.description="Stream-based alerting daemon"
LABEL org.opencontainers.image.source="https://github.com/zuchka/ding"
LABEL org.opencontainers.image.licenses="MIT"
LABEL org.opencontainers.image.url="https://ding.ing"
COPY --from=builder /ding /ding
EXPOSE 8080
ENTRYPOINT ["/ding", "serve", "--config", "/etc/ding/ding.yaml"]
