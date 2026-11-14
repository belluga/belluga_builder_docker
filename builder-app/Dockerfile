FROM golang:1.23-alpine AS go-test
WORKDIR /src
RUN apk add --no-cache git
COPY service/ ./
RUN go vet ./... && go test ./...

FROM go-test AS go-build
ARG BUILDER_SOURCE_SHA=unknown
RUN CGO_ENABLED=0 go build -trimpath -ldflags "-X main.buildSourceSHA=${BUILDER_SOURCE_SHA}" -o /out/viewer .

# Narrow, real-API proof for a selected Project's published visual artifacts.
# The fixed local-proof matrix supplies the exact selection and uses --no-cache.
FROM go-test AS visual-readiness-test
ARG BUILDER_VISUAL_READINESS_BASE_URL
ARG BUILDER_VISUAL_READINESS_COMPANY_ID
ARG BUILDER_VISUAL_READINESS_PROJECT_ID
ARG BUILDER_VISUAL_READINESS_PROTOTYPE_SHA
ARG BUILDER_VISUAL_READINESS_DESIGN_SYSTEM_SHA
ARG BUILDER_VISUAL_READINESS_PROTOTYPE_IDS
ARG BUILDER_VISUAL_READINESS_DESIGN_SYSTEM_ID
ARG BUILDER_VISUAL_READINESS_MIN_PROTOTYPES
ARG BUILDER_VISUAL_READINESS_INVENTORY_DIGEST
RUN test -n "$BUILDER_VISUAL_READINESS_BASE_URL" \
    && test -n "$BUILDER_VISUAL_READINESS_COMPANY_ID" \
    && test -n "$BUILDER_VISUAL_READINESS_PROJECT_ID" \
    && test -n "$BUILDER_VISUAL_READINESS_PROTOTYPE_SHA" \
    && test -n "$BUILDER_VISUAL_READINESS_DESIGN_SYSTEM_SHA" \
    && test -n "$BUILDER_VISUAL_READINESS_PROTOTYPE_IDS" \
    && test -n "$BUILDER_VISUAL_READINESS_DESIGN_SYSTEM_ID" \
    && test -n "$BUILDER_VISUAL_READINESS_MIN_PROTOTYPES" \
    && test -n "$BUILDER_VISUAL_READINESS_INVENTORY_DIGEST" \
    && go test -count=1 -run '^TestSelectedProjectVisualArtifactReadiness$' ./...

FROM node:24-alpine AS web-test
WORKDIR /web
COPY web/package.json web/package-lock.json ./
RUN npm ci --ignore-scripts --no-audit --no-fund
COPY web/ ./
RUN npm test -- --maxWorkers=1 && npm run build

FROM alpine:3.22 AS runtime
RUN addgroup -S viewer && adduser -S -G viewer viewer
COPY --from=go-build /out/viewer /app/viewer
COPY --from=web-test /web/dist /web
USER viewer
ENV LISTEN_ADDR=0.0.0.0:8080 WEB_ROOT=/web
EXPOSE 8080
ENTRYPOINT ["/app/viewer"]
