FROM golang:1.23-alpine AS test
RUN apk add --no-cache git
WORKDIR /src/go-app
COPY go-app/go.mod go-app/go.sum ./
COPY go-app/vendor ./vendor
COPY go-app/cmd ./cmd
COPY go-app/internal ./internal
COPY go-app/config ./config
COPY builder-app/service/go.mod /src/builder-app/service/go.mod
COPY builder-app/service/artifact_catalog /src/builder-app/service/artifact_catalog
RUN gofmt -w ./cmd ./internal /src/builder-app/service/artifact_catalog
RUN go vet -mod=vendor ./... && go test -mod=vendor ./...

FROM test AS build
RUN CGO_ENABLED=0 go build -mod=vendor -trimpath -o /out/registry-api ./cmd/registry-api

FROM alpine:3.22 AS runtime
RUN apk add --no-cache git ca-certificates
RUN addgroup -S registry && adduser -S -G registry registry
RUN mkdir /prototype-cache && chown registry:registry /prototype-cache
COPY --from=build /out/registry-api /app/registry-api
COPY --from=build /src/go-app/config /app/config
USER registry
ENV LOCAL_PROOF_MATRIX=/app/config/selected_todo_local_checks.json
ENV LISTEN_ADDR=0.0.0.0:8080
ENV TMPDIR=/tmp
EXPOSE 8080
ENTRYPOINT ["/app/registry-api"]
