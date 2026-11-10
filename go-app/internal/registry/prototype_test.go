package registry

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	ac "delphi-local-api/artifactcatalog"
)

func TestPrototypeAllowlistIncludesDeclaredNavigationAndEvidencePathsOnly(t *testing.T) {
	item := ac.Item{
		AllowedPaths: []string{"prototypes/demo/prototype.json", "prototypes/demo/index.html", "prototypes/demo/media/cover.webp"},
		Related:      []ac.Related{{Kind: "todo", Path: "todos/active/evidence.md"}},
		Screens:      []ac.Screen{{States: []ac.State{{ApprovedReferences: []ac.ApprovedReference{{ApprovalEvidence: "evidence/approval.md"}}}}}},
	}
	allowed := prototypeAllowedPaths(PrototypeCatalogResponse{navigation: []ac.Item{item}})
	for _, path := range []string{"prototypes/demo/prototype.json", "prototypes/demo/index.html", "prototypes/demo/media/cover.webp", "todos/active/evidence.md", "evidence/approval.md"} {
		if _, ok := allowed[path]; !ok {
			t.Errorf("declared path %q was not admitted", path)
		}
	}
	for _, path := range []string{"prototypes/demo/secret.env", "../outside", "/etc/passwd"} {
		if _, ok := allowed[path]; ok {
			t.Errorf("undeclared or unsafe path %q was admitted", path)
		}
	}
}

func TestPrototypeFileCacheCopiesBytesAndEvictsOldEntries(t *testing.T) {
	cache := newPrototypeFileCache()
	first := prototypeFileResponse{Data: []byte("first"), ContentType: "text/plain"}
	cache.put("first", first)
	first.Data[0] = 'X'
	read, ok := cache.get("first")
	if !ok || string(read.Data) != "first" {
		t.Fatalf("cache did not copy inserted bytes: %#v, %t", read, ok)
	}
	read.Data[0] = 'Y'
	readAgain, _ := cache.get("first")
	if string(readAgain.Data) != "first" {
		t.Fatalf("cache returned shared mutable bytes: %q", readAgain.Data)
	}
	for index := 0; index < prototypeFileCacheEntries; index++ {
		cache.put("entry-"+string(rune('a'+index)), prototypeFileResponse{Data: []byte("x")})
	}
	if _, ok := cache.get("first"); ok {
		t.Fatal("oldest cache entry was not evicted at the entry bound")
	}
}

type prototypeHTTPTestReader struct{}

type prototypeHTTPTestService struct {
	Service
	prototypeHTTPTestReader
}

func (prototypeHTTPTestReader) ReadPrototypes(context.Context, string, string) (PrototypeCatalogResponse, error) {
	return PrototypeCatalogResponse{}, nil
}
func (prototypeHTTPTestReader) ReadPrototype(context.Context, string, string, string, string) (PrototypeReadResponse, error) {
	return PrototypeReadResponse{}, nil
}
func (prototypeHTTPTestReader) ReadPrototypeFile(context.Context, string, string, string, string) (prototypeFileResponse, error) {
	return prototypeFileResponse{Data: []byte("<main>ok</main>"), ContentType: "text/html", ETag: `"revision-content"`, Revision: strings.Repeat("a", 40), InventoryDigest: "sha256:" + strings.Repeat("b", 64)}, nil
}

func TestPrototypeFileRouteSupportsHeadAndConditionalGet(t *testing.T) {
	handler := NewHandler(prototypeHTTPTestService{})
	path := "/internal/v1/companies/company-a/projects/project-a/prototypes/" + strings.Repeat("a", 40) + "/files/prototypes/demo/index.html"
	head := httptest.NewRecorder()
	handler.ServeHTTP(head, httptest.NewRequest(http.MethodHead, path, nil))
	if head.Code != http.StatusOK || head.Body.Len() != 0 || head.Header().Get("Content-Length") != "15" || head.Header().Get("X-Prototype-Revision") != strings.Repeat("a", 40) || head.Header().Get("X-Prototype-Inventory-Digest") == "" {
		t.Fatalf("HEAD response status=%d headers=%v body=%q", head.Code, head.Header(), head.Body.String())
	}
	conditional := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, path, nil)
	request.Header.Set("If-None-Match", `"revision-content"`)
	handler.ServeHTTP(conditional, request)
	if conditional.Code != http.StatusNotModified || conditional.Body.Len() != 0 {
		t.Fatalf("conditional GET status=%d body=%q", conditional.Code, conditional.Body.String())
	}
}

var _ http.Handler = NewHandler(prototypeHTTPTestService{})
