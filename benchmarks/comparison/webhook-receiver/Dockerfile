FROM golang:1.22-alpine AS build
WORKDIR /app
COPY main.go .
RUN go mod init webhook-receiver && go build -o webhook-receiver .

FROM scratch
COPY --from=build /app/webhook-receiver /webhook-receiver
ENTRYPOINT ["/webhook-receiver"]
