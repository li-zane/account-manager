FROM golang:1.23-alpine AS builder

WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/account-manager ./cmd/server

FROM alpine:3.21
RUN apk add --no-cache postgresql-client \
    && addgroup -S app \
    && adduser -S -G app app
COPY --from=builder /out/account-manager /usr/local/bin/account-manager
USER app
EXPOSE 8080
ENTRYPOINT ["/usr/local/bin/account-manager"]
