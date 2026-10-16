package main

import (
	"flag"
	"log"
	"net"
	"net/http"
	"os"
	"strings"
	"time"
)

func main() {
	if stateRoot := strings.TrimSpace(os.Getenv("DELPHI_BUILDER_STATE_ROOT")); stateRoot != "" {
		publicHost := strings.TrimSpace(os.Getenv("PUBLIC_PREVIEW_HOST"))
		address := env("LISTEN_ADDR", "127.0.0.1:8080")
		host, _, err := net.SplitHostPort(address)
		if err != nil || (!isLoopback(host) && os.Getenv("ALLOW_CONTAINER_BIND") != "true") {
			log.Fatal("listener must use a loopback address")
		}
		log.Fatal((&http.Server{Addr: address, Handler: registeredHandler(stateRoot, env("WEB_ROOT", "/web"), publicHost), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 10 * time.Second, WriteTimeout: 105 * time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 1 << 16}).ListenAndServe())
	}
	if len(os.Args) > 1 && os.Args[1] == "export" {
		flags := flag.NewFlagSet("export", flag.ExitOnError)
		repo := flags.String("repo", "", "trusted Foundation git checkout")
		commit := flags.String("commit", approvedCommit, "approved Foundation commit")
		out := flags.String("out", "", "new bundle destination")
		_ = flags.Parse(os.Args[2:])
		if *repo == "" || *out == "" {
			log.Fatal("repo and out are required")
		}
		if err := exportCommit(*repo, *commit, *out); err != nil {
			log.Fatal(err)
		}
		return
	}
	address := env("LISTEN_ADDR", "127.0.0.1:8080")
	host, _, err := net.SplitHostPort(address)
	if err != nil || (!isLoopback(host) && os.Getenv("ALLOW_CONTAINER_BIND") != "true") {
		log.Fatal("listener must use a loopback address")
	}
	bundle, err := loadBundle(env("BUNDLE_DIR", "/bundle"))
	if err != nil {
		log.Fatal("approved preview bundle is invalid")
	}
	mux := http.NewServeMux()
	mux.Handle("/api/preview", bundle.apiHandler())
	mux.Handle("/api/preview/documents/", bundle.documentHandler())
	mux.Handle("/", staticHandler(env("WEB_ROOT", "/web")))
	server := &http.Server{Addr: address, Handler: secureHeaders(mux), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 10 * time.Second, WriteTimeout: 15 * time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 1 << 16}
	log.Fatal(server.ListenAndServe())
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
	consumer := &registeredPreview{root: stateRoot}
	mux := http.NewServeMux()
	mux.HandleFunc("/api/local-preview", consumer.apiHandler)
	mux.HandleFunc("/snapshot/", consumer.artifactHandler)
	mux.Handle("/", localStaticHandler(webRoot))
	return secureHeaders(previewHostOnly(mux, publicHost))
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
