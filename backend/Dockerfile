FROM golang:1.25-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -o /out/server ./cmd/server

FROM alpine:3.21
# ca-certificates 供 upstream HTTPS 下发面校验证书，tzdata 让日志时间戳可读。
RUN apk add --no-cache ca-certificates tzdata && adduser -D -u 10001 msr
COPY --from=build /out/server /usr/local/bin/server
USER msr
EXPOSE 8080
ENTRYPOINT ["/usr/local/bin/server"]
