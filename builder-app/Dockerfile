FROM golang:1.23-alpine AS go-test
WORKDIR /src
RUN apk add --no-cache git
COPY service/ ./
RUN go vet ./... && go test ./...

FROM go-test AS go-build
RUN CGO_ENABLED=0 go build -trimpath -o /out/viewer .

FROM node:24-alpine AS web-test
WORKDIR /web
COPY web/package.json web/package-lock.json ./
RUN npm ci --ignore-scripts --no-audit --no-fund
COPY web/ ./
RUN npm test && npm run build

FROM alpine:3.22 AS exporter
RUN apk add --no-cache git ca-certificates
COPY --from=go-build /out/viewer /app/viewer
ENTRYPOINT ["/app/viewer", "export"]

FROM alpine:3.22 AS runtime
RUN addgroup -S viewer && adduser -S -G viewer viewer
COPY --from=go-build /out/viewer /app/viewer
COPY --from=web-test /web/dist /web
USER viewer
ENV LISTEN_ADDR=0.0.0.0:8080 BUNDLE_DIR=/bundle WEB_ROOT=/web
EXPOSE 8080
ENTRYPOINT ["/app/viewer"]
