package main

import (
	"context"
	"errors"
	"io"
	"log"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type serviceConfig struct {
	listen    string
	origin    string
	companies []string
	transport transportConfig
}

func configFromEnvironment(get func(string) string) (serviceConfig, error) {
	config := serviceConfig{
		listen: get("BUILDER_MCP_LISTEN_ADDR"),
		origin: get("BUILDER_MCP_REGISTRY_URL"),
		transport: transportConfig{
			bearer:  get("BUILDER_MCP_BEARER_TOKEN"),
			hosts:   allowlist(get("BUILDER_MCP_ALLOWED_HOSTS")),
			origins: allowlist(get("BUILDER_MCP_ALLOWED_ORIGINS")),
		},
	}
	if config.listen == "" {
		config.listen = "127.0.0.1:8098"
	}
	if _, _, err := net.SplitHostPort(config.listen); err != nil {
		return serviceConfig{}, errors.New("invalid Builder MCP listen address")
	}
	if len(config.transport.hosts) == 0 {
		config.transport.hosts = allowlist("127.0.0.1:8098,localhost:8098")
	}
	secret := config.transport.bearer
	if len(secret) < 32 || strings.IndexFunc(secret, func(r rune) bool { return r <= 0x20 || r >= 0x7f }) >= 0 {
		return serviceConfig{}, errors.New("Builder MCP requires a separate bearer of at least 32 visible ASCII bytes")
	}
	for company := range allowlist(get("BUILDER_MCP_COMPANY_IDS")) {
		if !identityID.MatchString(company) {
			return serviceConfig{}, errors.New("invalid Company ID in MCP Company allowlist")
		}
		config.companies = append(config.companies, company)
	}
	if len(config.companies) == 0 || len(config.companies) > 64 {
		return serviceConfig{}, errors.New("Builder MCP requires between 1 and 64 allowed Company IDs")
	}
	sort.Strings(config.companies)
	return config, nil
}

func allowlist(raw string) map[string]bool {
	values := map[string]bool{}
	for _, value := range strings.Split(raw, ",") {
		if value = strings.TrimSpace(value); value != "" {
			values[value] = true
		}
	}
	return values
}

var builderProtocolVersions = []string{"2026-07-28", "2025-11-25"}

func newHandler(config serviceConfig) (http.Handler, error) {
	registry, err := newRegistryClient(config.origin)
	if err != nil {
		return nil, err
	}
	tools, err := loadCatalog()
	if err != nil {
		return nil, err
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	server := mcp.NewServer(&mcp.Implementation{Name: "builder", Version: "1.0.0"}, &mcp.ServerOptions{
		Capabilities:              &mcp.ServerCapabilities{Tools: &mcp.ToolCapabilities{}},
		PageSize:                  catalogSize,
		Logger:                    logger,
		SupportedProtocolVersions: builderProtocolVersions,
	})
	adapter := adapter{registry: registry, companies: config.companies, companyList: strings.Join(config.companies, ",")}
	for _, tool := range tools {
		destructive := tool.isDestructive()
		server.AddTool(&mcp.Tool{
			Name: tool.Name, Description: tool.Description, InputSchema: tool.Schema,
			Annotations: &mcp.ToolAnnotations{
				ReadOnlyHint: tool.Method == http.MethodGet, DestructiveHint: &destructive,
			},
		}, adapter.handler(tool))
	}
	handler := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return server }, &mcp.StreamableHTTPOptions{
		Stateless: true, JSONResponse: true, Logger: logger, MaxRequestBodyBytes: maxMCPRequest, PropagateRequestCancellation: true,
	})
	return protectTransport(config.transport, handler), nil
}

func newHTTPServer(listen string, handler http.Handler) *http.Server {
	return &http.Server{
		Addr: listen, Handler: handler,
		ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 200 * time.Second, WriteTimeout: 200 * time.Second,
		IdleTimeout: 30 * time.Second, MaxHeaderBytes: 16 << 10,
	}
}

func main() {
	config, err := configFromEnvironment(os.Getenv)
	if err != nil {
		log.Fatal(err)
	}
	handler, err := newHandler(config)
	if err != nil {
		log.Fatal(err)
	}
	server := newHTTPServer(config.listen, handler)
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	serve := make(chan error, 1)
	go func() { serve <- server.ListenAndServe() }()
	select {
	case <-ctx.Done():
		deadline, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := server.Shutdown(deadline); err != nil {
			log.Print("Builder MCP graceful shutdown deadline exceeded")
			_ = server.Close()
		}
	case err := <-serve:
		if !errors.Is(err, http.ErrServerClosed) {
			log.Fatal("Builder MCP listener failed")
		}
	}
}
