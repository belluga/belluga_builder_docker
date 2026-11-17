package registry

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"image"
	"image/color"
	"image/png"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	ac "delphi-local-api/artifactcatalog"
	"go.mongodb.org/mongo-driver/v2/bson"
)

type storedTODOImage struct {
	path     string
	data     []byte
	reads    int
	revision string
	onRead   func()
}

type storedTODOImageSource struct {
	ac.Source
	image *storedTODOImage
}

func (s storedTODOImageSource) Read(path string, _ int64) ([]byte, ac.FileMode, error) {
	s.image.reads++
	if onRead := s.image.onRead; onRead != nil {
		s.image.onRead = nil
		onRead()
	}
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
	if g.image.revision != "" {
		return g.image.revision, nil
	}
	return strings.Repeat("a", 40), nil
}
func (g storedTODOImageGit) OpenRevision(_ context.Context, company, project string, binding RepositoryBinding, revision string) (prototypeGitSource, error) {
	head := g.image.revision
	if head == "" {
		head = strings.Repeat("a", 40)
	}
	if company != "company-a" || project != "builder" || binding != g.binding || revision != head {
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
	content := validTODOContent()
	record.Content = &content
	forgedRecord := record
	forgedContent := *record.Content
	forgedContent.ImageAttachments = []TODOImageAttachment{{ID: "image-forged", Path: "todo-reference-images/company-a/builder/image-reference-todo/" + strings.Repeat("a", 64) + ".png", Digest: "sha256:" + strings.Repeat("a", 64), VerifiedRevision: strings.Repeat("b", 40)}}
	forgedRecord.Content = &forgedContent
	if _, err := store.AdmitTODO(ctx, "company-a", "builder", TODOAdmissionRequest{SchemaVersion: todoCreationSchemaVersion, OperationID: "create-forged-image-reference", TODO: forgedRecord}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("TODO creation accepted forged image metadata: %v", err)
	}
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
	if _, err := store.AttachTODOImage(ctx, "company-a", request); !errors.Is(err, ErrConflict) {
		t.Fatalf("unpublished image linked: %v", err)
	}
	before, err := store.ReadTODO(ctx, "company-a", "builder", record.ID, nil)
	if err != nil || len(before.TODO.Content.ImageAttachments) != 0 || before.TODORevision != 0 {
		t.Fatalf("unpublished image changed TODO: %+v %v", before, err)
	}
	// The agent publishes the exact file on the bound Git branch before POST.
	sum := sha256.Sum256(request.Data)
	imageStore.path = "todo-reference-images/company-a/builder/" + record.ID + "/" + hex.EncodeToString(sum[:]) + ".png"
	imageStore.data = []byte("different bytes at the required Git path")
	if _, err := store.AttachTODOImage(ctx, "company-a", request); !errors.Is(err, ErrConflict) {
		t.Fatalf("divergent Git bytes were linked: %v", err)
	}
	before, err = store.ReadTODO(ctx, "company-a", "builder", record.ID, nil)
	if err != nil || len(before.TODO.Content.ImageAttachments) != 0 || before.TODORevision != 0 {
		t.Fatalf("divergent Git bytes changed TODO: %+v %v", before, err)
	}
	imageStore.data = append([]byte(nil), request.Data...)
	attached, err := store.AttachTODOImage(ctx, "company-a", request)
	if err != nil {
		t.Fatal(err)
	}
	if attached.NoOp || attached.TODORevision != 1 || attached.ContentRevision != 1 || attached.Attachment.Path != imageStore.path || attached.Attachment.Digest == "" || attached.ImageURL != "/api/todo-images/company-a/builder/image-reference-todo/"+attached.Attachment.ID || imageStore.reads < 1 {
		t.Fatalf("wrong linked attachment: %+v", attached)
	}
	read, err := store.ReadTODO(ctx, "company-a", "builder", record.ID, nil)
	if err != nil || len(read.TODO.Content.ImageAttachments) != 1 || read.TODO.Content.ImageAttachments[0] != attached.Attachment {
		t.Fatalf("metadata missing: %+v %v", read, err)
	}
	database, err := store.todoTenantDatabase(ctx, "company-a")
	if err != nil {
		t.Fatal(err)
	}
	var rawVersion bson.M
	if err := database.Collection(todoContentVersionsCollection).FindOne(ctx, bson.D{{Key: "project_id", Value: "builder"}, {Key: "todo_id", Value: record.ID}, {Key: "revision", Value: int64(1)}}).Decode(&rawVersion); err != nil {
		t.Fatal(err)
	}
	asDocument := func(value any) map[string]any {
		result := map[string]any{}
		switch document := value.(type) {
		case bson.M:
			for key, item := range document {
				result[key] = item
			}
		case bson.D:
			for _, item := range document {
				result[item.Key] = item.Value
			}
		}
		return result
	}
	rawContent := asDocument(rawVersion["content"])
	if len(rawContent) == 0 {
		t.Fatalf("content version is not BSON document: %T", rawVersion["content"])
	}
	rawAttachments, ok := rawContent["image_attachments"].(bson.A)
	if !ok || len(rawAttachments) != 1 {
		t.Fatalf("content version does not contain one metadata attachment: %T %+v", rawContent["image_attachments"], rawContent["image_attachments"])
	}
	rawAttachment := asDocument(rawAttachments[0])
	if len(rawAttachment) != 5 {
		t.Fatalf("attachment contains unexpected fields: %+v", rawAttachments[0])
	}
	for _, key := range []string{"id", "path", "digest", "verified_revision", "description"} {
		if _, ok := rawAttachment[key].(string); !ok {
			t.Fatalf("attachment metadata %s is missing or not text: %+v", key, rawAttachment)
		}
	}
	if !strings.Contains(read.ImageUploadTeach, "--network builder-registry-private") || !strings.Contains(read.ImageUploadTeach, "realpath -e") || !strings.Contains(read.ImageUploadTeach, "-F \"image=@/todo-image\"") || !strings.Contains(read.ImageUploadTeach, "first publish its exact bytes") || strings.Contains(read.ImageUploadTeach, "local-api/run") {
		t.Fatalf("agent upload instructions missing from TODO readback: %q", read.ImageUploadTeach)
	}
	file, err := store.ReadTODOImage(ctx, "company-a", "builder", record.ID, attached.Attachment.ID)
	if err != nil || !bytes.Equal(file.Data, request.Data) || file.ContentType != "image/png" {
		t.Fatalf("image readback failed: %+v %v", file, err)
	}
	reads := imageStore.reads
	if _, err := store.ReadTODOImage(ctx, "company-a", "builder", record.ID, attached.Attachment.ID); err != nil || imageStore.reads != reads {
		t.Fatalf("repeated image read did not use bounded Git cache: reads=%d before=%d err=%v", imageStore.reads, reads, err)
	}
	// A new branch head is observed after the verified-byte cache expires.
	imageStore.revision = strings.Repeat("b", 40)
	store.todoImages.mu.Lock()
	for key, entry := range store.todoImages.entries {
		entry.expires = time.Now().Add(-time.Second)
		store.todoImages.entries[key] = entry
	}
	store.todoImages.mu.Unlock()
	file, err = store.ReadTODOImage(ctx, "company-a", "builder", record.ID, attached.Attachment.ID)
	if err != nil || !bytes.Equal(file.Data, request.Data) || imageStore.reads != reads+1 {
		t.Fatalf("advanced Git branch was not revalidated: reads=%d err=%v", imageStore.reads, err)
	}
	imageStore.revision = strings.Repeat("c", 40)
	imageStore.data = nil
	store.todoImages = todoImageCache{} // Registry restart clears the verified-byte cache.
	if _, err := store.ReadTODOImage(ctx, "company-a", "builder", record.ID, attached.Attachment.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("removed Git file remained readable after cache reset: %v", err)
	}
	imageStore.revision = strings.Repeat("d", 40)
	imageStore.data = []byte("different Git bytes")
	store.todoImages = todoImageCache{}
	if _, err := store.ReadTODOImage(ctx, "company-a", "builder", record.ID, attached.Attachment.ID); !errors.Is(err, ErrConflict) {
		t.Fatalf("divergent Git file was served: %v", err)
	}
	imageStore.data = append([]byte(nil), request.Data...)
	store.todoImages = todoImageCache{}
	if _, err := store.ReadTODOImage(ctx, "company-a", "builder", "other-todo", attached.Attachment.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("cross-TODO read: %v", err)
	}
	noOp, err := store.AttachTODOImage(ctx, "company-a", request)
	if err != nil || !noOp.NoOp || noOp.TODORevision != 1 || noOp.ImageURL != attached.ImageURL {
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
	forged := contentFromTODO(updated.TODO)
	forged.ImageAttachments = append([]TODOImageAttachment(nil), forged.ImageAttachments...)
	forged.ImageAttachments[0].Description = "forged description"
	if _, err := store.EditTODOContent(ctx, "company-a", TODOContentEditRequest{ProjectID: "builder", TODOID: record.ID, ExpectedRevision: int64Pointer(updated.TODORevision), OperationID: "forge-image-description", Content: forged, Reason: "Try changing protected metadata"}); !errors.Is(err, ErrConflict) {
		t.Fatalf("generic edit altered image description: %v", err)
	}
	restored, err := store.RestoreTODOContent(ctx, "company-a", TODOContentRestoreRequest{ProjectID: "builder", TODOID: record.ID, ExpectedRevision: int64Pointer(updated.TODORevision), OperationID: "restore-before-image", ContentRevision: 0, Reason: "Restore original TODO text"})
	if err != nil {
		t.Fatalf("restore original content: %v", err)
	}
	restoredRead, err := store.ReadTODO(ctx, "company-a", "builder", record.ID, nil)
	if err != nil || restoredRead.TODORevision != restored.Revision || len(restoredRead.TODO.Content.ImageAttachments) != 1 || restoredRead.TODO.Content.ImageAttachments[0] != attached.Attachment {
		t.Fatalf("generic restore changed protected image metadata: %+v %v", restoredRead, err)
	}
	// Publication can win while another TODO writer changes the Mongo revision.
	var secondPNG bytes.Buffer
	canvas := image.NewRGBA(image.Rect(0, 0, 2, 2))
	canvas.Set(0, 0, color.RGBA{R: 255, A: 255})
	if err := png.Encode(&secondPNG, canvas); err != nil {
		t.Fatal(err)
	}
	secondDigest := sha256.Sum256(secondPNG.Bytes())
	imageStore.path = "todo-reference-images/company-a/builder/" + record.ID + "/" + hex.EncodeToString(secondDigest[:]) + ".png"
	imageStore.data = secondPNG.Bytes()
	secondRequest := TODOImageUploadRequest{ProjectID: "builder", TODOID: record.ID, ExpectedRevision: restoredRead.TODORevision, OperationID: "second-image-after-concurrent-edit", Data: secondPNG.Bytes()}
	imageStore.onRead = func() {
		concurrent := contentFromTODO(restoredRead.TODO)
		concurrent.Title += " while Git image is published"
		if _, err := store.EditTODOContent(ctx, "company-a", TODOContentEditRequest{ProjectID: "builder", TODOID: record.ID, ExpectedRevision: int64Pointer(restoredRead.TODORevision), OperationID: "concurrent-text-edit", Content: concurrent, Reason: "Advance TODO during Git verification"}); err != nil {
			t.Fatalf("concurrent TODO edit: %v", err)
		}
	}
	if _, err := store.AttachTODOImage(ctx, "company-a", secondRequest); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale CAS after Git publication was not rejected: %v", err)
	}
	afterRace, err := store.ReadTODO(ctx, "company-a", "builder", record.ID, nil)
	if err != nil || afterRace.TODORevision != restoredRead.TODORevision+1 || len(afterRace.TODO.Content.ImageAttachments) != 1 {
		t.Fatalf("failed CAS changed image metadata: %+v %v", afterRace, err)
	}
	secondRequest.ExpectedRevision = afterRace.TODORevision
	secondAttached, err := store.AttachTODOImage(ctx, "company-a", secondRequest)
	if err != nil || secondAttached.NoOp || secondAttached.TODORevision != afterRace.TODORevision+1 {
		t.Fatalf("retry after CAS failure did not attach image: %+v %v", secondAttached, err)
	}
	secondRetry, err := store.AttachTODOImage(ctx, "company-a", secondRequest)
	if err != nil || !secondRetry.NoOp || secondRetry.TODORevision != secondAttached.TODORevision {
		t.Fatalf("retry duplicated second image: %+v %v", secondRetry, err)
	}
	approvedRecord, err := readSelectedTODO(ctx, database.Collection(todosCollection), "builder", record.ID)
	if err != nil {
		t.Fatal(err)
	}
	approvalProof := TODOProofReference{Kind: "test-stage-proof", Source: "test-fixture", Reference: "ready-before-image", SourceDigest: sourceHash([]byte("ready-before-image")), VerificationState: "verified"}
	ready, err := writeVerifiedTODO(ctx, database, database.Collection(todosCollection), "company-a", approvedRecord, "Ready-for-Implementation", approvalProof, "seed-ready-before-image", "transition", sourceHash([]byte("seed-ready-before-image")))
	if err != nil {
		t.Fatalf("seed approved stage: %v", err)
	}
	var thirdPNG bytes.Buffer
	canvas.Set(1, 1, color.RGBA{G: 255, A: 255})
	if err := png.Encode(&thirdPNG, canvas); err != nil {
		t.Fatal(err)
	}
	thirdDigest := sha256.Sum256(thirdPNG.Bytes())
	imageStore.path = "todo-reference-images/company-a/builder/" + record.ID + "/" + hex.EncodeToString(thirdDigest[:]) + ".png"
	imageStore.data = thirdPNG.Bytes()
	thirdAttached, err := store.AttachTODOImage(ctx, "company-a", TODOImageUploadRequest{ProjectID: "builder", TODOID: record.ID, ExpectedRevision: ready.TODORevision, OperationID: "third-image-after-approval", Data: thirdPNG.Bytes()})
	if err != nil {
		t.Fatalf("attach image after approved stage: %v", err)
	}
	finalRead, err := store.ReadTODO(ctx, "company-a", "builder", record.ID, nil)
	if err != nil || finalRead.TODORevision != thirdAttached.TODORevision || finalRead.TODO.Stage != "Planning" || finalRead.TODO.VerifiedTransitionRef == nil || finalRead.TODO.VerifiedTransitionRef.SourceDigest != thirdAttached.ContentDigest || len(finalRead.TODO.Content.ImageAttachments) != 3 {
		t.Fatalf("post-approval image did not reopen Planning: %+v %v", finalRead, err)
	}
}
