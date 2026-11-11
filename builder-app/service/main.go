package main

import (
	"encoding/json"
	"log"
	"net"
	"net/http"
	"os"
	"regexp"
	"strings"
	"time"
)

var buildSourceSHA = "unknown"
var buildSHAFormat = regexp.MustCompile(`^[a-f0-9]{40}$`)

func main() {
	stateRoot := strings.TrimSpace(os.Getenv("DELPHI_BUILDER_STATE_ROOT"))
	if stateRoot != "" {
		publicHost := strings.TrimSpace(os.Getenv("PUBLIC_PREVIEW_HOST"))
		address := env("LISTEN_ADDR", "127.0.0.1:8080")
		host, _, err := net.SplitHostPort(address)
		if err != nil || (!isLoopback(host) && os.Getenv("ALLOW_CONTAINER_BIND") != "true") {
			log.Fatal("listener must use a loopback address")
		}
		log.Fatal((&http.Server{Addr: address, Handler: registeredHandler(stateRoot, env("WEB_ROOT", "/web"), publicHost), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 10 * time.Second, WriteTimeout: 105 * time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 1 << 16}).ListenAndServe())
	}
	log.Fatal("DELPHI_BUILDER_STATE_ROOT is required; legacy Foundation bundle and snapshot serving are retired")
}

func isLoopback(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func localHostOnly(next http.Handler) http.Handler {
	return previewHostOnly(next, "")
}

func registeredHandler(stateRoot, webRoot, publicHost string) http.Handler {
	consumer := &registeredPreview{}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/build", func(w http.ResponseWriter, _ *http.Request) {
		if !buildSHAFormat.MatchString(buildSourceSHA) {
			writeError(w, http.StatusServiceUnavailable, "build_revision_unavailable")
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		_ = json.NewEncoder(w).Encode(struct {
			SourceSHA string `json:"source_sha"`
		}{SourceSHA: buildSourceSHA})
	})
	mux.HandleFunc("/api/catalog", consumer.catalogAPIHandler)
	mux.HandleFunc("/api/local-preview", retiredSnapshotHandler)
	mux.HandleFunc("/api/local-todo-source", retiredTODOSourceHandler)
	mux.HandleFunc("/api/roadmap", consumer.roadmapAPIHandler)
	mux.HandleFunc("/api/overview", consumer.overviewAPIHandler)
	mux.HandleFunc("/api/overview/media/", consumer.overviewAPIHandler)
	mux.HandleFunc("/api/knowledge", consumer.knowledgeAPIHandler)
	mux.HandleFunc("/api/knowledge/index", consumer.knowledgeIndexAPIHandler)
	mux.HandleFunc("/api/design-system", consumer.designSystemAPIHandler)
	mux.HandleFunc("/api/design-system/assets", consumer.designSystemAPIHandler)
	mux.HandleFunc("/api/design-system/presentation/", consumer.designSystemAPIHandler)
	mux.HandleFunc("/api/prototypes", consumer.prototypeAPIHandler)
	mux.HandleFunc("/api/prototypes/", consumer.prototypeAPIHandler)
	mux.HandleFunc("/api/landing", consumer.landingAPIHandler)
	mux.HandleFunc("/api/landing/file/", consumer.landingAPIHandler)
	mux.HandleFunc("/api/portfolios", consumer.portfolioAPIHandler)
	mux.HandleFunc("/api/portfolios/file/", consumer.portfolioAPIHandler)
	mux.HandleFunc("/snapshot/", retiredSnapshotHandler)
	mux.Handle("/", localStaticHandler(webRoot))
	return secureHeaders(previewHostOnly(mux, publicHost))
}

func retiredSnapshotHandler(w http.ResponseWriter, _ *http.Request) {
	writeError(w, http.StatusGone, "project_snapshot_retired")
}

func retiredTODOSourceHandler(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusGone, map[string]string{
		"error": "todo_source_retired",
		"teach": "TEACH: TODO content is available through the exact Project TODO API; snapshot-backed TODO source transport is retired.",
	})
}

func previewHostOnly(next http.Handler, publicHost string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		host := r.Host
		if strings.Contains(host, ":") {
			var err error
			host, _, err = net.SplitHostPort(host)
			if err != nil {
				writeError(w, http.StatusForbidden, "local_host_required")
				return
			}
		}
		if !isLoopback(host) && (publicHost == "" || !strings.EqualFold(host, publicHost)) {
			writeError(w, http.StatusForbidden, "local_host_required")
			return
		}
		next.ServeHTTP(w, r)
	})
}

func env(key, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(key)); value != "" {
		return value
	}
	return fallback
}

func secureHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self'; img-src 'self' data:; connect-src 'self'; object-src 'none'; base-uri 'none'; frame-ancestors 'none'")
		next.ServeHTTP(w, r)
	})
}
