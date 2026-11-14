package main

import (
	"errors"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"belluga-builder-go/internal/registry"
)

func TestRegistryServerReturnsResponseAfterOldWriteDeadline(t *testing.T) {
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		timer := time.NewTimer(21 * time.Second)
		defer timer.Stop()
		select {
		case <-timer.C:
			_, _ = io.WriteString(w, "complete-arrival-ok")
		case <-r.Context().Done():
		}
	})
	server := newRegistryServer("", handler)
	if server.ReadTimeout != 10*time.Second || server.WriteTimeout != 110*time.Second {
		t.Fatalf("Registry HTTP budget = read %s, write %s", server.ReadTimeout, server.WriteTimeout)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	served := make(chan error, 1)
	go func() { served <- server.Serve(listener) }()
	defer func() {
		_ = server.Close()
		if err := <-served; err != nil && !errors.Is(err, http.ErrServerClosed) {
			t.Errorf("Registry HTTP server: %v", err)
		}
	}()
	client := &http.Client{Timeout: 30 * time.Second}
	response, err := client.Get("http://" + listener.Addr().String())
	if err != nil {
		t.Fatalf("delayed Registry response: %v", err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	if err != nil || response.StatusCode != http.StatusOK || string(body) != "complete-arrival-ok" {
		t.Fatalf("delayed Registry response = status %d body %q error %v", response.StatusCode, body, err)
	}
}

func TestBoundRoadmapPathUsesRegisteredFoundation(t *testing.T) {
	workspace := t.TempDir()
	path := filepath.Join(workspace, "project", "foundation", "roadmap", "catalog.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(`{"schema_version":"builder-roadmap-v1"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	resolved, err := boundRoadmapPath(&registry.Binding{WorkspaceRoot: workspace, ProjectRoot: "project", FoundationRoot: "foundation"})
	if err != nil || resolved != path {
		t.Fatalf("bound roadmap path = %q, %v; want %q", resolved, err, path)
	}
}

func TestProvisionSelectedTODOsRequiresConfiguredRegistryCredential(t *testing.T) {
	t.Setenv("REGISTRY_MONGODB_URI", "")
	err := runProvisionSelectedTODOs(nil)
	if err == nil || !strings.Contains(err.Error(), "REGISTRY_MONGODB_URI") {
		t.Fatalf("provisioning did not require the configured registry credential: %v", err)
	}
}

func TestProvisionSelectedTODOsRejectsUnboundedArguments(t *testing.T) {
	t.Setenv("REGISTRY_MONGODB_URI", "")
	err := runProvisionSelectedTODOs([]string{"--company-id", "another-tenant"})
	if err == nil || !strings.Contains(err.Error(), "bounded to belluga-solutions") {
		t.Fatalf("provisioning accepted caller-selected tenant: %v", err)
	}
}

func TestProvisionRegisteredTODOsRequiresCredentialAndRejectsTenantArguments(t *testing.T) {
	t.Setenv("REGISTRY_MONGODB_URI", "")
	if err := runProvisionRegisteredTODOs(nil); err == nil || !strings.Contains(err.Error(), "REGISTRY_MONGODB_URI") {
		t.Fatalf("missing Registry credential was accepted: %v", err)
	}
	if err := runProvisionRegisteredTODOs([]string{"unifast"}); err == nil || !strings.Contains(err.Error(), "accepts no tenant arguments") {
		t.Fatalf("caller-selected Company was accepted: %v", err)
	}
}

func TestBoundRoadmapPathRejectsWorkspaceEscape(t *testing.T) {
	workspace := t.TempDir()
	if _, err := boundRoadmapPath(&registry.Binding{WorkspaceRoot: workspace, ProjectRoot: "..", FoundationRoot: "foundation"}); err == nil {
		t.Fatal("accepted Project binding outside its registered workspace")
	}
}
