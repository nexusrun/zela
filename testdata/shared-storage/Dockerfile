FROM golang:1.25-alpine AS build
WORKDIR /src
COPY app.go .
RUN CGO_ENABLED=0 go build -o /app app.go
FROM scratch
COPY --from=build /app /app
USER 1000:1000
HEALTHCHECK --interval=2s --timeout=2s --start-period=2s --retries=3 CMD ["/app", "health"]
ENTRYPOINT ["/app"]
