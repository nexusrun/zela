FROM golang:1.26-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY cmd/ ./cmd/
COPY internal/ ./internal/
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /zela-server ./cmd/zela-server
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags="-s -w" -o /workers/zela-worker-linux-amd64 ./cmd/worker
RUN CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -trimpath -ldflags="-s -w" -o /workers/zela-worker-linux-arm64 ./cmd/worker

FROM alpine:3.23
RUN apk add --no-cache ca-certificates openssh-client && addgroup -g 10001 zela && adduser -D -u 10001 -G zela zela && mkdir /data && chown zela:zela /data
COPY --from=build /zela-server /usr/local/bin/zela-server
COPY --from=build /workers /opt/zela/workers
USER zela
VOLUME /data
EXPOSE 8080
ENTRYPOINT ["zela-server"]
CMD ["--listen", "0.0.0.0:8080", "--data", "/data", "--worker-binaries", "/opt/zela/workers"]
