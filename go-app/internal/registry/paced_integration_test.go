//go:build integration

package registry

import (
	"context"
	"fmt"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

func TestPACEDReaderUpdatesMongoSnapshotPointerThroughPrivateAPI(t *testing.T) {
	uri := os.Getenv("REGISTRY_TEST_MONGODB_URI")
	if uri == "" {
		t.Skip("isolated MongoDB fixture not configured")
	}
	ctx := context.Background()
	stamp := fmt.Sprintf("reader_%d", time.Now().UnixNano())
	store, err := OpenMongo(ctx, uri, stamp)
	if err != nil {
		t.Fatal(err)
	}
	store.tenantPrefix = stamp
	companyID, projectID := "company-"+fmt.Sprint(time.Now().UnixNano()), "project-"+fmt.Sprint(time.Now().UnixNano())
	if _, err := store.Register(ctx, Registration{CompanyID: companyID, CompanyName: "Reader Integration", ProjectID: projectID, ProjectName: "Reader Integration Project", Binding: validTestBinding()}); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(NewHandler(store))
	t.Cleanup(func() {
		server.Close()
		_ = store.client.Database(store.tenantDatabase(companyID)).Drop(context.Background())
		_ = store.control.Drop(context.Background())
		_ = store.Close(context.Background())
	})
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	repoRoot := filepath.Clean(filepath.Join(cwd, "../../.."))
	pacedRoot := filepath.Join(repoRoot, "delphi-ai", "local-api")
	command := exec.Command("go", "test", "./internal/project_reader", "-run", "^TestReaderPersistsSnapshotPointerThroughMongoRegistryAPI$", "-count=1")
	command.Dir = pacedRoot
	command.Env = append(os.Environ(), "REGISTRY_READER_TEST_API_URL="+server.URL, "REGISTRY_READER_TEST_COMPANY_ID="+companyID, "REGISTRY_READER_TEST_PROJECT_ID="+projectID)
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("PACED Reader-to-API-to-Mongo integration failed: %v\n%s", err, output)
	}
}
