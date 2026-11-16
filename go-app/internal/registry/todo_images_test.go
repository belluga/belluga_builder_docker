package registry

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"image"
	"image/color"
	"image/png"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	ac "delphi-local-api/artifactcatalog"
)

type testTODOImageSource struct {
	ac.Source
	data []byte
}

func (s testTODOImageSource) Read(_ string, _ int64) ([]byte, ac.FileMode, error) {
	return s.data, ac.ModeRegular, nil
}
func (s testTODOImageSource) VerifyStable() error { return nil }

type testTODOImageGit struct{ data []byte }

func (g testTODOImageGit) BranchHead(context.Context, RepositoryBinding) (string, error) {
	return strings.Repeat("a", 40), nil
}
func (g testTODOImageGit) OpenRevision(context.Context, string, string, RepositoryBinding, string) (prototypeGitSource, error) {
	return prototypeGitSource{source: testTODOImageSource{data: g.data}}, nil
}

type testTODOImageTransport func(*http.Request) (*http.Response, error)

func (f testTODOImageTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestTODOImagePublisherWritesBoundRepositoryBranch(t *testing.T) {
	t.Setenv("DELPHI_GITHUB_WRITE_TOKEN", "test-token")
	data := []byte("image bytes")
	path := "todo-reference-images/company-a/project-a/todo-a/" + strings.Repeat("a", 64) + ".png"
	client := &http.Client{Transport: testTODOImageTransport(func(r *http.Request) (*http.Response, error) {
		if r.Method != http.MethodPut || r.URL.Host != "api.github.com" || r.URL.Path != "/repos/owner/repo/contents/"+path || r.Header.Get("Authorization") != "Bearer test-token" {
			t.Fatalf("unexpected GitHub request: %s %s", r.Method, r.URL)
		}
		var body struct {
			Content string `json:"content"`
			Branch  string `json:"branch"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		decoded, err := base64.StdEncoding.DecodeString(body.Content)
		if err != nil || !bytes.Equal(decoded, data) || body.Branch != "main" {
			t.Fatalf("wrong publication payload: branch=%q data=%q err=%v", body.Branch, decoded, err)
		}
		return &http.Response{StatusCode: http.StatusCreated, Body: io.NopCloser(strings.NewReader("{}")), Header: make(http.Header)}, nil
	})}
	if err := (githubTODOImagePublisher{client: client}).Publish(context.Background(), RepositoryBinding{State: "configured", RepositoryID: "owner/repo", PublicationBranch: "main"}, path, data); err != nil {
		t.Fatal(err)
	}
}

func TestTODOImageGitReadbackRequiresExactDigest(t *testing.T) {
	data := []byte("Git image bytes")
	hash := sha256.Sum256(data)
	store := &MongoStore{prototypeGit: testTODOImageGit{data: data}}
	path := "todo-reference-images/company-a/project-a/todo-a/" + hex.EncodeToString(hash[:]) + ".png"
	revision, err := store.verifyTODOImageGit(context.Background(), "company-a", "project-a", RepositoryBinding{}, path, "sha256:"+hex.EncodeToString(hash[:]))
	if err != nil || revision != strings.Repeat("a", 40) {
		t.Fatalf("verified Git bytes rejected: %q %v", revision, err)
	}
	if _, err := store.verifyTODOImageGit(context.Background(), "company-a", "project-a", RepositoryBinding{}, path, "sha256:"+strings.Repeat("b", 64)); err != ErrConflict {
		t.Fatalf("digest mismatch returned %v", err)
	}
}

type testTODOImageHTTPService struct {
	sessionHTTPService
	uploads int
	reads   int
}

func (s *testTODOImageHTTPService) AttachTODOImage(_ context.Context, companyID string, request TODOImageUploadRequest) (TODOImageUploadResponse, error) {
	s.uploads++
	if companyID != "company-a" || request.ProjectID != "project-a" || request.TODOID != "todo-a" || request.ExpectedRevision != 2 || request.OperationID != "upload-a" {
		return TODOImageUploadResponse{}, ErrConflict
	}
	return TODOImageUploadResponse{CompanyID: companyID, ProjectID: request.ProjectID, TODOID: request.TODOID, TODORevision: 3}, nil
}

func (s *testTODOImageHTTPService) ReadTODOImage(_ context.Context, companyID, projectID, todoID, imageID string) (todoImageFile, error) {
	s.reads++
	if companyID != "company-a" || projectID != "project-a" || todoID != "todo-a" || imageID != "image-a" {
		return todoImageFile{}, ErrNotFound
	}
	return todoImageFile{Data: []byte("test"), ContentType: "image/png", Digest: "sha256:" + strings.Repeat("a", 64)}, nil
}

func testTODOImageMultipart(t *testing.T) (string, []byte) {
	t.Helper()
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	for key, value := range map[string]string{"expected_revision": "2", "operation_id": "upload-a"} {
		if err := writer.WriteField(key, value); err != nil {
			t.Fatal(err)
		}
	}
	file, err := writer.CreateFormFile("image", "reference.png")
	if err != nil {
		t.Fatal(err)
	}
	_, _ = file.Write([]byte("test"))
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	return writer.FormDataContentType(), body.Bytes()
}

func TestTODOImageHTTPRequiresBoundSessionAndExactPath(t *testing.T) {
	service := &testTODOImageHTTPService{sessionHTTPService: sessionHTTPService{session: Session{Type: "codex", ExternalID: "conversation-a", Binding: SessionProjectBinding{CompanyID: "company-a", ProjectID: "project-a"}}}}
	handler := NewHandler(service)
	contentType, body := testTODOImageMultipart(t)
	request := func(project string, bound bool) *http.Request {
		r := httptest.NewRequest(http.MethodPost, "/internal/v1/companies/company-a/projects/"+project+"/todos/todo-a/images", bytes.NewReader(body))
		r.Header.Set("Content-Type", contentType)
		if bound {
			r.Header.Set(sessionLaneHeader, "conversation")
			r.Header.Set(sessionTypeHeader, "codex")
			r.Header.Set(sessionExternalIDHeader, "conversation-a")
		}
		return r
	}
	for _, tc := range []struct {
		project string
		bound   bool
		status  int
	}{{"project-a", false, http.StatusBadRequest}, {"project-b", true, http.StatusForbidden}, {"project-a", true, http.StatusOK}} {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request(tc.project, tc.bound))
		if response.Code != tc.status {
			t.Fatalf("project=%s bound=%t: status=%d body=%s", tc.project, tc.bound, response.Code, response.Body.String())
		}
	}
	if service.uploads != 1 {
		t.Fatalf("upload called %d times, want only the bound request", service.uploads)
	}
	for _, method := range []string{http.MethodGet, http.MethodHead} {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest(method, "/internal/v1/companies/company-a/projects/project-a/todos/todo-a/images/image-a", nil))
		if response.Code != http.StatusOK || response.Header().Get("Content-Type") != "image/png" || method == http.MethodHead && response.Body.Len() != 0 {
			t.Fatalf("%s read failed: %d %s", method, response.Code, response.Body.String())
		}
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/internal/v1/companies/company-a/projects/project-b/todos/todo-a/images/image-a", nil))
	if response.Code != http.StatusNotFound {
		t.Fatalf("cross-project image read status=%d", response.Code)
	}
}

func TestTODOImageDataAcceptsImageAndRejectsTruncatedBytes(t *testing.T) {
	var encoded bytes.Buffer
	canvas := image.NewRGBA(image.Rect(0, 0, 2, 2))
	canvas.Set(0, 0, color.RGBA{R: 255, A: 255})
	if err := png.Encode(&encoded, canvas); err != nil {
		t.Fatal(err)
	}
	digest, ext, mime, err := todoImageData(encoded.Bytes())
	if err != nil || !strings.HasPrefix(digest, "sha256:") || ext != "png" || mime != "image/png" {
		t.Fatalf("valid PNG rejected: %q %q %q %v", digest, ext, mime, err)
	}
	truncated := encoded.Bytes()[:len(encoded.Bytes())-5]
	if _, _, _, err := todoImageData(truncated); err == nil {
		t.Fatal("truncated image accepted")
	}
}

func TestTODOImageAttachmentContractRequiresGitMetadata(t *testing.T) {
	content := validTODOContent()
	content.ImageAttachments = []TODOImageAttachment{{
		ID: "image-a", Path: "todo-reference-images/company-a/project-a/todo-a/" + strings.Repeat("a", 64) + ".png",
		Digest: "sha256:" + strings.Repeat("a", 64), VerifiedRevision: strings.Repeat("b", 40),
	}}
	if err := ValidateTODOContent(content, false); err != nil {
		t.Fatalf("valid attachment rejected: %v", err)
	}
	content.ImageAttachments[0].Path = "../image.png"
	if err := ValidateTODOContent(content, false); err == nil {
		t.Fatal("unmanaged path accepted")
	}
}
