FROM golang:1.23-alpine AS test
WORKDIR /src
COPY go.mod go.sum ./
COPY cmd ./cmd
COPY internal ./internal
RUN go mod download
RUN gofmt -w ./cmd ./internal
RUN go vet ./... && go test ./...

FROM test AS build
RUN CGO_ENABLED=0 go build -trimpath -o /out/registry-api ./cmd/registry-api

FROM alpine:3.22 AS runtime
RUN addgroup -S registry && adduser -S -G registry registry
COPY --from=build /out/registry-api /app/registry-api
USER registry
ENV LISTEN_ADDR=0.0.0.0:8080
EXPOSE 8080
ENTRYPOINT ["/app/registry-api"]
