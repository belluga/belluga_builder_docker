FROM golang:1.26-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY *.go catalog.json ./
RUN CGO_ENABLED=0 go build -trimpath -mod=readonly -o /out/builder-mcp .

FROM alpine:3.22 AS runtime
RUN addgroup -S mcp && adduser -S -G mcp mcp
COPY --from=build /out/builder-mcp /app/builder-mcp
USER mcp
ENV BUILDER_MCP_LISTEN_ADDR=0.0.0.0:8098
ENV GOMEMLIMIT=768MiB
EXPOSE 8098
ENTRYPOINT ["/app/builder-mcp"]
