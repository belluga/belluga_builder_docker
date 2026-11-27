package registry

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"image"
	"image/color"
	"image/png"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	ac "delphi-local-api/artifactcatalog"
)

type portfolioCatalogGitFixture struct {
	root, revision string
	headErr        error
}

func (g portfolioCatalogGitFixture) BranchHead(context.Context, RepositoryBinding) (string, error) {
	if g.headErr != nil {
		return "", g.headErr
	}
	return g.revision, nil
}

func (g portfolioCatalogGitFixture) OpenRevision(_ context.Context, _, _ string, _ RepositoryBinding, revision string) (prototypeGitSource, error) {
	source, err := ac.NewGitTree(g.root, revision)
	if err != nil {
		return prototypeGitSource{}, err
	}
	return prototypeGitSource{source: source}, nil
}

func TestMongoPortfolioCatalogEmitsTEACHForNotAuthoredInvalidAndUnavailable(t *testing.T) {
	for _, tc := range []struct {
		name, manifest, state, code string
		unavailable                 bool
	}{{"not-authored", "", "not_authored", "portfolio_not_authored", false}, {"invalid", `{"schema_version":"2","summary":"invalid"}`, "invalid", "portfolio_invalid", false}, {"unavailable", `{"schema_version":"1","summary":"valid"}`, "unavailable", "portfolio_unavailable", true}} {
		t.Run(tc.name, func(t *testing.T) {
			store, ctx := overviewMongoFixture(t, "portfolio-"+tc.name)
			manifest := tc.manifest
			if tc.name == "not-authored" {
				manifest = `{"schema_version":"1","summary":"valid"}`
			}
			root, revision, _ := portfolioFixture(t, manifest)
			if tc.name == "not-authored" {
				if err := os.Remove(filepath.Join(root, "project_portfolio.json")); err != nil {
					t.Fatal(err)
				}
				if err := os.Remove(filepath.Join(root, "project_portfolio", "cover.png")); err != nil {
					t.Fatal(err)
				}
				commitPortfolio(t, root)
				revision = gitPortfolio(t, root, "rev-parse", "HEAD")
			}
			current, err := store.ReadRepositoryBinding(ctx, "company-overview", "project-a")
			if err != nil {
				t.Fatal(err)
			}
			if _, err := store.ChangeRepositoryBinding(ctx, "company-overview", "project-a", RepositoryBindingRequest{Action: "replace", ProjectID: "project-a", ExpectedRevision: current.Revision, OperationID: "portfolio-bind-" + tc.name, Actor: "owner", RepositoryID: "belluga/repo", Branch: "main"}); err != nil {
				t.Fatal(err)
			}
			git := portfolioCatalogGitFixture{root: root, revision: revision}
			if tc.unavailable {
				git.headErr = ErrUnavailable
			}
			store.prototypeGit = git
			response := httptest.NewRecorder()
			NewHandler(store).ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/internal/v1/companies/company-overview/portfolios", nil))
			var catalog PortfolioCatalogResponse
			if err := json.Unmarshal(response.Body.Bytes(), &catalog); err != nil || response.Code != http.StatusOK || len(catalog.Projects) != 1 || catalog.Projects[0].State != tc.state || catalog.Projects[0].TeachDetail == nil || catalog.Projects[0].TeachDetail.Code != tc.code || catalog.Projects[0].TeachDetail.Effects != TeachEffectsNotApplicable {
				t.Fatalf("portfolio catalog state=%+v status=%d err=%v body=%s", catalog.Projects, response.Code, err, response.Body)
			}
		})
	}
}

func TestPortfolioV1ProjectionUsesPinnedGitBytesAndOptionalCover(t *testing.T) {
	root, revision, binding := portfolioFixture(t, `{"schema_version":"1","summary":"A bounded portfolio summary"}`)
	source, err := ac.NewGitTree(root, revision)
	if err != nil {
		t.Fatal(err)
	}
	got, err := evaluatePortfolioSource(source, "company-a", "project-a", binding, revision)
	if err != nil {
		t.Fatal(err)
	}
	if got.State != "available" || got.CompanyID != "company-a" || got.ProjectID != "project-a" || got.Revision != revision || got.BindingRevision != binding.Revision || got.Summary != "A bounded portfolio summary" || got.InventoryDigest == "" || len(got.Files) != 2 {
		t.Fatalf("portfolio projection is not bound to exact Git content: %+v", got)
	}
}

func TestPortfolioDistinguishesMissingInvalidAndUnavailableContent(t *testing.T) {
	root, revision, binding := portfolioFixture(t, `{"schema_version":"1","summary":"Valid"}`)
	if err := os.Remove(filepath.Join(root, "project_portfolio.json")); err != nil {
		t.Fatal(err)
	}
	commitPortfolio(t, root)
	revision = gitPortfolio(t, root, "rev-parse", "HEAD")
	source, _ := ac.NewGitTree(root, revision)
	_, err := evaluatePortfolioSource(source, "company-a", "project-a", binding, revision)
	if err != nil {
		t.Fatalf("cover-only portfolio is independently valid, got %v", err)
	}
	if err := os.Remove(filepath.Join(root, "project_portfolio", "cover.png")); err != nil {
		t.Fatal(err)
	}
	commitPortfolio(t, root)
	revision = gitPortfolio(t, root, "rev-parse", "HEAD")
	source, _ = ac.NewGitTree(root, revision)
	_, err = evaluatePortfolioSource(source, "company-a", "project-a", binding, revision)
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("both absent should be not-authored, got %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "project_portfolio.json"), []byte(`{"schema_version":"2","summary":"unsupported"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	commitPortfolio(t, root)
	revision = gitPortfolio(t, root, "rev-parse", "HEAD")
	source, _ = ac.NewGitTree(root, revision)
	_, err = evaluatePortfolioSource(source, "company-a", "project-a", binding, revision)
	if !errors.Is(err, ErrInvalid) {
		t.Fatalf("unsupported portfolio should be invalid, got %v", err)
	}
	if _, err = evaluatePortfolioSource(source, "other-company", "project-a", binding, revision); !errors.Is(err, ErrInvalid) {
		t.Fatalf("cross-Company projection admitted: %v", err)
	}
}

func TestPortfolioRejectsDuplicateKeysUnsafePathsAndOversize(t *testing.T) {
	for _, body := range []string{
		`{"schema_version":"1","schema_version":"1","summary":"duplicate"}`,
		`{"schema_version":"1","summary":""}`,
		`{"schema_version":"1","summary":"` + strings.Repeat("x", 241) + `"}`,
	} {
		root, revision, binding := portfolioFixture(t, body)
		source, _ := ac.NewGitTree(root, revision)
		if _, err := evaluatePortfolioSource(source, "company-a", "project-a", binding, revision); !errors.Is(err, ErrInvalid) {
			t.Fatalf("invalid portfolio body admitted: %v", err)
		}
	}
	for _, path := range []string{"../secret", "project_portfolio/../secret", "/etc/passwd", "project_portfolio/cover.svg"} {
		if portfolioMIMEType(path) != "" {
			t.Errorf("unsafe portfolio path admitted: %q", path)
		}
	}
}

func portfolioFixture(t *testing.T, manifest string) (string, string, RepositoryBinding) {
	t.Helper()
	root := t.TempDir()
	gitPortfolio(t, root, "init", "--quiet")
	gitPortfolio(t, root, "config", "user.email", "test@example.invalid")
	gitPortfolio(t, root, "config", "user.name", "Portfolio Test")
	if err := os.WriteFile(filepath.Join(root, "project_portfolio.json"), []byte(manifest), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "project_portfolio"), 0o700); err != nil {
		t.Fatal(err)
	}
	var cover bytes.Buffer
	coverImage := image.NewRGBA(image.Rect(0, 0, 2, 2))
	coverImage.Set(0, 0, color.RGBA{R: 0x11, G: 0x22, B: 0x33, A: 0xff})
	if err := png.Encode(&cover, coverImage); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "project_portfolio", "cover.png"), cover.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	commitPortfolio(t, root)
	return root, gitPortfolio(t, root, "rev-parse", "HEAD"), RepositoryBinding{RepositoryID: "owner/repo", PublicationBranch: "main", State: "configured", Revision: 7}
}

func commitPortfolio(t *testing.T, root string) {
	t.Helper()
	gitPortfolio(t, root, "add", "--all")
	gitPortfolio(t, root, "commit", "--quiet", "-m", "portfolio fixture")
}

func gitPortfolio(t *testing.T, root string, args ...string) string {
	t.Helper()
	command := exec.Command("git", append([]string{"-C", root}, args...)...)
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v: %s", args, err, output)
	}
	return strings.TrimSpace(string(output))
}
