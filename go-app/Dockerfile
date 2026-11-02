FROM golang:1.23-alpine AS test
WORKDIR /src
COPY go.mod go.sum ./
COPY cmd ./cmd
COPY internal ./internal
COPY config ./config
RUN go mod download
RUN gofmt -w ./cmd ./internal
RUN go vet ./... && go test ./...

FROM test AS build
RUN CGO_ENABLED=0 go build -trimpath -o /out/registry-api ./cmd/registry-api

FROM alpine:3.22 AS runtime
RUN addgroup -S registry && adduser -S -G registry registry
COPY --from=build /out/registry-api /app/registry-api
COPY --from=build /src/config /app/config
USER registry
ENV LOCAL_PROOF_MATRIX=/app/config/selected_todo_local_checks.json
ENV LISTEN_ADDR=0.0.0.0:8080
EXPOSE 8080
ENTRYPOINT ["/app/registry-api"]
