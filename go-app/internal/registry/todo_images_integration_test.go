package registry

import (
	"bytes"
	"context"
	"errors"
	"image"
	"image/png"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	ac "delphi-local-api/artifactcatalog"
)

type storedTODOImage struct {
	path  string
	data  []byte
	reads int
}

type storedTODOImagePublisher struct {
	image   *storedTODOImage
	publish bool
}

func (p storedTODOImagePublisher) Publish(_ context.Context, _ RepositoryBinding, path string, data []byte) error {
	if p.publish {
		p.image.path, p.image.data = path, append([]byte(nil), data...)
	}
	return nil
}

type storedTODOImageSource struct {
	ac.Source
	image *storedTODOImage
}

func (s storedTODOImageSource) Read(path string, _ int64) ([]byte, ac.FileMode, error) {
	s.image.reads++
	if path != s.image.path || s.image.data == nil {
		return nil, 0, ErrNotFound
	}
	return append([]byte(nil), s.image.data...), ac.ModeRegular, nil
}
func (s storedTODOImageSource) VerifyStable() error { return nil }

type storedTODOImageGit struct {
	image   *storedTODOImage
	binding RepositoryBinding
}

func (g storedTODOImageGit) BranchHead(_ context.Context, binding RepositoryBinding) (string, error) {
	if binding != g.binding {
		return "", ErrConflict
	}
	return strings.Repeat("a", 40), nil
}
func (g storedTODOImageGit) OpenRevision(_ context.Context, company, project string, binding RepositoryBinding, revision string) (prototypeGitSource, error) {
	if company != "company-a" || project != "builder" || binding != g.binding || revision != strings.Repeat("a", 40) {
		return prototypeGitSource{}, ErrConflict
	}
	return prototypeGitSource{source: storedTODOImageSource{image: g.image}}, nil
}

func TestMongoTODOImageUploadLinksOnlyVerifiedGitBytes(t *testing.T) {
	uri := os.Getenv("REGISTRY_TEST_MONGODB_URI")
	if uri == "" {
		t.Skip("isolated replica-set MongoDB fixture not configured")
	}
	ctx := context.Background()
	stamp := strconv.FormatInt(time.Now().UnixNano(), 36)
	store, err := OpenMongo(ctx, uri, stamp)
	if err != nil {
		t.Fatal(err)
	}
	store.tenantPrefix = stamp
	t.Cleanup(func() {
		_ = store.client.Database(store.tenantDatabase("company-a")).Drop(context.Background())
		_ = store.control.Drop(context.Background())
		_ = store.Close(context.Background())
	})
	if _, err := registerTestProjectWithBinding(ctx, store, Registration{CompanyID: "company-a", CompanyName: "Company A", ProjectID: "builder", ProjectName: "Builder", Binding: validTestBinding()}); err != nil {
		t.Fatal(err)
	}
	if err := store.ProvisionTODOCollection(ctx, "company-a"); err != nil {
		t.Fatal(err)
	}
	if err := store.ProvisionTODOHistory(ctx, "company-a"); err != nil {
		t.Fatal(err)
	}
	change := func(action, id, epic string, revision int64) {
		_, err := store.ChangeRoadmap(ctx, "company-a", action, RoadmapChange{ProjectID: "builder", ExpectedRoadmapRevision: &revision, OperationID: "image-" + action, ID: id, EpicID: epic, Name: "Test " + id, Description: "Image reference fixture"})
		if err != nil {
			t.Fatal(err)
		}
	}
	change("create-epic", "e-01", "", 0)
	change("create-release", "r-01", "e-01", 1)
	record := mongoBornTODOFixture()
	record.ID, record.ReleaseID = "image-reference-todo", "r-01"
	record.Position = intPointer(0)
	if _, err := store.AdmitTODO(ctx, "company-a", "builder", TODOAdmissionRequest{SchemaVersion: todoCreationSchemaVersion, OperationID: "create-image-reference", TODO: record}); err != nil {
		t.Fatal(err)
	}
	var encoded bytes.Buffer
	if err := png.Encode(&encoded, image.NewRGBA(image.Rect(0, 0, 2, 2))); err != nil {
		t.Fatal(err)
	}
	imageStore := &storedTODOImage{}
	binding, err := store.ReadRepositoryBinding(ctx, "company-a", "builder")
	if err != nil {
		t.Fatal(err)
	}
	store.prototypeGit = storedTODOImageGit{image: imageStore, binding: binding}
	request := TODOImageUploadRequest{ProjectID: "builder", TODOID: record.ID, ExpectedRevision: 0, OperationID: "upload-image-reference", Description: "AccessScope selector", Data: encoded.Bytes()}
	store.todoImagePublisher = storedTODOImagePublisher{image: imageStore}
	if _, err := store.AttachTODOImage(ctx, "company-a", request); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("unpublished image linked: %v", err)
	}
	before, err := store.ReadTODO(ctx, "company-a", "builder", record.ID, nil)
	if err != nil || len(before.TODO.Content.ImageAttachments) != 0 || before.TODORevision != 0 {
		t.Fatalf("unpublished image changed TODO: %+v %v", before, err)
	}
	store.todoImagePublisher = storedTODOImagePublisher{image: imageStore, publish: true}
	attached, err := store.AttachTODOImage(ctx, "company-a", request)
	if err != nil {
		t.Fatal(err)
	}
	if attached.NoOp || attached.TODORevision != 1 || attached.ContentRevision != 1 || attached.Attachment.Path != imageStore.path || attached.Attachment.Digest == "" || imageStore.reads < 1 {
		t.Fatalf("wrong linked attachment: %+v", attached)
	}
	read, err := store.ReadTODO(ctx, "company-a", "builder", record.ID, nil)
	if err != nil || len(read.TODO.Content.ImageAttachments) != 1 || read.TODO.Content.ImageAttachments[0] != attached.Attachment {
		t.Fatalf("metadata missing: %+v %v", read, err)
	}
	file, err := store.ReadTODOImage(ctx, "company-a", "builder", record.ID, attached.Attachment.ID)
	if err != nil || !bytes.Equal(file.Data, request.Data) || file.ContentType != "image/png" {
		t.Fatalf("image readback failed: %+v %v", file, err)
	}
	reads := imageStore.reads
	if _, err := store.ReadTODOImage(ctx, "company-a", "builder", record.ID, attached.Attachment.ID); err != nil || imageStore.reads != reads {
		t.Fatalf("repeated image read did not use bounded Git cache: reads=%d before=%d err=%v", imageStore.reads, reads, err)
	}
	if _, err := store.ReadTODOImage(ctx, "company-a", "builder", "other-todo", attached.Attachment.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("cross-TODO read: %v", err)
	}
	noOp, err := store.AttachTODOImage(ctx, "company-a", request)
	if err != nil || !noOp.NoOp || noOp.TODORevision != 1 {
		t.Fatalf("same operation retry: %+v %v", noOp, err)
	}
	request.Description = "Changed description"
	if _, err := store.AttachTODOImage(ctx, "company-a", request); !errors.Is(err, ErrConflict) {
		t.Fatalf("operation reuse: %v", err)
	}
	request.OperationID, request.Description = "different-operation", "AccessScope selector"
	if _, err := store.AttachTODOImage(ctx, "company-a", request); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale revision: %v", err)
	}
	authored := contentFromTODO(read.TODO)
	authored.Title += " clarified"
	currentRevision := read.TODORevision
	if _, err := store.EditTODOContent(ctx, "company-a", TODOContentEditRequest{ProjectID: "builder", TODOID: record.ID, ExpectedRevision: &currentRevision, OperationID: "edit-after-image", Content: authored, Reason: "Clarify TODO"}); err != nil {
		t.Fatal(err)
	}
	updated, err := store.ReadTODO(ctx, "company-a", "builder", record.ID, nil)
	if err != nil || len(updated.TODO.Content.ImageAttachments) != 1 || updated.TODO.Content.ImageAttachments[0] != attached.Attachment {
		t.Fatalf("generic edit lost image: %+v %v", updated, err)
	}
}
