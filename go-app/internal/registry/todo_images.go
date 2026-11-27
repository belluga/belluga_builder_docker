package registry

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"image"
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"
	"regexp"
	"strings"
	"time"

	ac "delphi-local-api/artifactcatalog"
)

const maxTODOImageBytes = 2 << 20

const todoImageTeach = "If this TODO defines a Prototype screen, assess whether its text reference depends on visual details, especially specific screen elements and widgets. A description, file path or URL alone does not attach an image. If an image would help, consider publishing it to the bound artifact Git branch and linking it through this TODO's multipart /images API with expected_revision and operation_id. This is optional; the text reference remains valid."
const todoImageAttachedTeach = "The image is attached to this TODO for study; it is not approved for Prototype use. To use it in a Prototype, follow the study and approval workflow, add it to Prototype assets, and record approved_references with approval_evidence also listed in related."

const todoImageUploadTeach = `To attach an optional local image, first publish its exact bytes to this Project's bound artifact Git repository and publication branch using the existing Project Git write workflow. Use the path todo-reference-images/<company_id>/<project_id>/<todo_id>/<sha256-of-file>.<png|jpg|gif>, with the extension matching the decoded image format. Verify the bound repository, branch, remote commit and path before calling the API. The Registry reads Git and links the verified file; it does not push to GitHub or need a GitHub write token.

Then use the private Go Registry API from the Builder host with Docker access. Set IMAGE_ABS to the same published image's absolute local path, COMPANY_ID, PROJECT_ID and TODO_ID to this TODO's exact IDs, TODO_REVISION to the current todo_revision, and OPERATION_ID to a stable ID. Use the current bound CODEX_SESSION_ID. The file must be a readable regular PNG, JPEG or GIF of 1 to 2097152 bytes; at most 16 images can be attached.

Run this host preflight before Docker:
case "$IMAGE_ABS" in /*) ;; *) printf 'image: use an absolute canonical path\n' >&2; exit 64;; esac
case "$IMAGE_ABS" in *[,:]*) printf 'image: use a path without comma or colon\n' >&2; exit 64;; esac
IMAGE_REAL=$(realpath -e -- "$IMAGE_ABS") || exit 64
[ "$IMAGE_ABS" = "$IMAGE_REAL" ] && [ -f "$IMAGE_ABS" ] && [ -r "$IMAGE_ABS" ] || { printf 'image: use a readable regular file without symlinks\n' >&2; exit 64; }
IMAGE_SIZE=$(stat -c %s -- "$IMAGE_ABS") || exit 64
[ "$IMAGE_SIZE" -ge 1 ] && [ "$IMAGE_SIZE" -le 2097152 ] || { printf 'image: use a file of 1 to 2097152 bytes\n' >&2; exit 64; }
[ -n "$CODEX_SESSION_ID" ] || { printf 'session: use the current bound CODEX_SESSION_ID\n' >&2; exit 64; }

Then send the file directly to the existing Go POST (add -F "description=<text>" only if useful):
docker run --rm --user "$(id -u):$(id -g)" --network builder-registry-private --mount "type=bind,source=${IMAGE_ABS},target=/todo-image,readonly" curlimages/curl:8.15.0 --fail-with-body -X POST -H "X-Delphi-Request-Lane: conversation" -H "X-Delphi-Session-Type: codex" -H "X-Delphi-Session-ID: ${CODEX_SESSION_ID}" -F "image=@/todo-image" -F "expected_revision=${TODO_REVISION}" -F "operation_id=${OPERATION_ID}" "http://builder-registry:8080/internal/v1/companies/${COMPANY_ID}/projects/${PROJECT_ID}/todos/${TODO_ID}/images"

Keep the same operation ID and image bytes when retrying. Open the returned image_url through the Builder viewer to see the pixels, then read this TODO again to confirm its attachment metadata. A text reference remains valid without an image. If the private network or bound session is unavailable, stop and restore access; do not report a completed upload. The public Builder viewer only serves image GET/HEAD.`

var todoImagePathPattern = regexp.MustCompile(`^todo-reference-images/[a-z][a-z0-9-]*/[a-z][a-z0-9-]*/[a-z][a-z0-9-]*/[a-f0-9]{64}\.(png|jpg|gif)$`)

func validTODOImagePath(value string) bool {
	return len(value) <= 1024 && todoImagePathPattern.MatchString(value)
}

type TODOImageUploadRequest struct {
	ProjectID        string
	TODOID           string
	ExpectedRevision int64
	OperationID      string
	Description      string
	Data             []byte
}

type TODOImageUploadResponse struct {
	CompanyID       string              `json:"company_id"`
	ProjectID       string              `json:"project_id"`
	TODOID          string              `json:"todo_id"`
	TODORevision    int64               `json:"todo_revision"`
	ContentRevision int64               `json:"content_revision"`
	ContentDigest   string              `json:"content_digest"`
	Attachment      TODOImageAttachment `json:"attachment"`
	ImageURL        string              `json:"image_url"`
	NoOp            bool                `json:"no_op"`
	Teach           string              `json:"teach"`
	TeachDetail     *TeachDiagnostic    `json:"teach_detail,omitempty"`
}

type todoImageFile struct {
	Data        []byte
	ContentType string
	Digest      string
}

func todoImageData(data []byte) (digest, extension, contentType string, err error) {
	if len(data) == 0 || len(data) > maxTODOImageBytes {
		return "", "", "", todoTeach("image", "provide a nonempty PNG, JPEG or GIF image up to 2 MiB", ErrInvalid)
	}
	config, format, decodeErr := image.DecodeConfig(bytes.NewReader(data))
	if decodeErr != nil || config.Width < 1 || config.Height < 1 || config.Width > 4096 || config.Height > 4096 || int64(config.Width)*int64(config.Height) > 16_000_000 {
		return "", "", "", todoTeach("image", "provide a valid PNG, JPEG or GIF image within 4096 pixels per side", ErrInvalid)
	}
	if _, _, decodeErr = image.Decode(bytes.NewReader(data)); decodeErr != nil {
		return "", "", "", todoTeach("image", "provide complete decodable image bytes", ErrInvalid)
	}
	switch format {
	case "png":
		extension, contentType = "png", "image/png"
	case "jpeg":
		extension, contentType = "jpg", "image/jpeg"
	case "gif":
		extension, contentType = "gif", "image/gif"
	default:
		return "", "", "", todoTeach("image", "provide a PNG, JPEG or GIF image", ErrInvalid)
	}
	hash := sha256.Sum256(data)
	return "sha256:" + hex.EncodeToString(hash[:]), extension, contentType, nil
}

func todoImageID(operationID string) string {
	hash := sha256.Sum256([]byte(operationID))
	return "image-" + hex.EncodeToString(hash[:16])
}

func todoImageURL(companyID, projectID, todoID, imageID string) string {
	return "/api/todo-images/" + companyID + "/" + projectID + "/" + todoID + "/" + imageID
}

func (m *MongoStore) verifyTODOImageGit(ctx context.Context, companyID, projectID string, binding RepositoryBinding, filePath, digest string) (string, error) {
	if m.prototypeGit == nil {
		return "", ErrUnavailable
	}
	revision, err := m.prototypeGit.BranchHead(ctx, binding)
	if err != nil {
		return "", err
	}
	source, err := m.prototypeGit.OpenRevision(ctx, companyID, projectID, binding, revision)
	if err != nil {
		return "", err
	}
	defer source.close()
	data, mode, err := source.source.Read(filePath, maxTODOImageBytes)
	if err != nil || mode != ac.ModeRegular || len(data) > maxTODOImageBytes || source.source.VerifyStable() != nil {
		return "", ErrNotFound
	}
	hash := sha256.Sum256(data)
	if digest != "sha256:"+hex.EncodeToString(hash[:]) {
		return "", ErrConflict
	}
	return revision, nil
}

func (m *MongoStore) AttachTODOImage(ctx context.Context, companyID string, request TODOImageUploadRequest) (TODOImageUploadResponse, error) {
	if !validID(companyID) || !validID(request.ProjectID) || !validID(request.TODOID) || request.ExpectedRevision < 0 || !todoOperationIDPattern.MatchString(request.OperationID) || request.Description != "" && !validSummaryField(request.Description, 1024) {
		return TODOImageUploadResponse{}, todoTeach("image_upload", "provide an exact Company/Project/TODO, current revision, stable operation ID and optional UTF-8 description", ErrInvalid)
	}
	digest, extension, _, err := todoImageData(request.Data)
	if err != nil {
		return TODOImageUploadResponse{}, err
	}
	imageID := todoImageID(request.OperationID)
	filePath := fmt.Sprintf("todo-reference-images/%s/%s/%s/%s.%s", companyID, request.ProjectID, request.TODOID, strings.TrimPrefix(digest, "sha256:"), extension)
	current, err := m.ReadTODO(ctx, companyID, request.ProjectID, request.TODOID, nil)
	if err != nil {
		return TODOImageUploadResponse{}, err
	}
	if current.TODO.Content == nil || current.ContentRevision == nil {
		return TODOImageUploadResponse{}, todoTeach("todo.content", "use a current typed TODO before attaching an image", ErrConflict)
	}
	for _, existing := range current.TODO.Content.ImageAttachments {
		if existing.ID == imageID {
			if existing.Path != filePath || existing.Digest != digest || existing.Description != request.Description {
				return TODOImageUploadResponse{}, todoTeach("operation_id", "not be reused for different image bytes or description", ErrConflict)
			}
			return TODOImageUploadResponse{CompanyID: companyID, ProjectID: request.ProjectID, TODOID: request.TODOID, TODORevision: current.TODORevision, ContentRevision: *current.ContentRevision, ContentDigest: current.ContentDigest, Attachment: existing, ImageURL: todoImageURL(companyID, request.ProjectID, request.TODOID, existing.ID), NoOp: true, Teach: todoImageAttachedTeach, TeachDetail: &TeachDiagnostic{SchemaVersion: teachDiagnosticSchemaVersion, Code: "todo_image_already_attached", FieldPath: "operation_id", NextAction: "Use the verified existing attachment returned for this exact operation ID.", OperationID: request.OperationID, Effects: TeachEffectsCommitted, Retry: TeachRetryNone}}, nil
		}
	}
	if request.ExpectedRevision != current.TODORevision {
		return TODOImageUploadResponse{}, &todoRevisionConflict{Revision: current.TODORevision}
	}
	if err := requireActiveTODO(current.TODO); err != nil {
		return TODOImageUploadResponse{}, err
	}
	if len(current.TODO.Content.ImageAttachments) >= 16 {
		return TODOImageUploadResponse{}, todoTeach("content.image_attachments", "contain fewer than 16 images before another upload", ErrConflict)
	}
	binding, err := m.ReadRepositoryBinding(ctx, companyID, request.ProjectID)
	if err != nil || binding.State != "configured" {
		return TODOImageUploadResponse{}, todoTeach("repository_binding", "configure the same Project artifact Git binding before upload", ErrConflict)
	}
	var lastVerifyErr error
	for attempt := 0; attempt < 3; attempt++ {
		revision, verifyErr := m.verifyTODOImageGit(ctx, companyID, request.ProjectID, binding, filePath, digest)
		if verifyErr == nil {
			currentBinding, bindingErr := m.ReadRepositoryBinding(ctx, companyID, request.ProjectID)
			if bindingErr != nil || currentBinding != binding {
				return TODOImageUploadResponse{}, todoTeach("repository_binding", "remain unchanged while the image is published and linked", ErrConflict)
			}
			attachment := TODOImageAttachment{ID: imageID, Path: filePath, Digest: digest, VerifiedRevision: revision, Description: request.Description}
			database, dbErr := m.todoTenantDatabase(ctx, companyID)
			if dbErr != nil {
				return TODOImageUploadResponse{}, dbErr
			}
			project, collection, selectErr := m.selectedTODOCollection(ctx, companyID, request.ProjectID, request.TODOID)
			if selectErr != nil {
				return TODOImageUploadResponse{}, selectErr
			}
			if selectErr = m.exactTODORelease(ctx, companyID, project, current.TODO); selectErr != nil {
				return TODOImageUploadResponse{}, selectErr
			}
			authored := contentFromTODO(current.TODO)
			descriptionHash := sha256.Sum256([]byte(request.Description))
			edit := TODOContentEditRequest{ProjectID: request.ProjectID, TODOID: request.TODOID, ExpectedRevision: &request.ExpectedRevision, OperationID: request.OperationID, Content: authored, Reason: "Attach Git image " + digest + " description " + hex.EncodeToString(descriptionHash[:])}
			requestDigest, digestErr := todoRequestDigest(edit)
			if digestErr != nil {
				return TODOImageUploadResponse{}, ErrInvalid
			}
			result, writeErr := m.writeTODOContent(ctx, database, collection, companyID, current.TODO, edit, requestDigest, "content_changed", true, &attachment)
			if writeErr != nil {
				return TODOImageUploadResponse{}, writeErr
			}
			written, readErr := m.ReadTODO(ctx, companyID, request.ProjectID, request.TODOID, &result.Revision)
			if readErr != nil || written.ContentRevision == nil || written.ContentDigest == "" {
				return TODOImageUploadResponse{}, ErrUnavailable
			}
			return TODOImageUploadResponse{CompanyID: companyID, ProjectID: request.ProjectID, TODOID: request.TODOID, TODORevision: written.TODORevision, ContentRevision: *written.ContentRevision, ContentDigest: written.ContentDigest, Attachment: attachment, ImageURL: todoImageURL(companyID, request.ProjectID, request.TODOID, attachment.ID), Teach: todoImageAttachedTeach, TeachDetail: &TeachDiagnostic{SchemaVersion: teachDiagnosticSchemaVersion, Code: "todo_image_attached", FieldPath: "content.image_attachments", NextAction: "Use the returned verified attachment and current TODO revision.", OperationID: request.OperationID, Effects: TeachEffectsCommitted, Retry: TeachRetryNone}}, nil
		}
		if errors.Is(verifyErr, ErrConflict) {
			return TODOImageUploadResponse{}, todoTeach("image", "match the bytes already published at the required Git path", ErrConflict)
		}
		lastVerifyErr = verifyErr
		if ctx.Err() != nil {
			return TODOImageUploadResponse{}, ErrUnavailable
		}
		if attempt < 2 {
			pause := time.NewTimer(time.Duration(attempt+1) * 100 * time.Millisecond)
			select {
			case <-pause.C:
			case <-ctx.Done():
				pause.Stop()
				return TODOImageUploadResponse{}, ErrUnavailable
			}
		}
	}
	if errors.Is(lastVerifyErr, ErrNotFound) {
		return TODOImageUploadResponse{}, todoTeach("image", "be published at the required path on the bound Git branch before linking the TODO", ErrConflict)
	}
	return TODOImageUploadResponse{}, todoTeach("image", "be readable from the bound Git branch before linking the TODO", ErrUnavailable)
}

func (m *MongoStore) ReadTODOImage(ctx context.Context, companyID, projectID, todoID, imageID string) (todoImageFile, error) {
	if !validID(imageID) {
		return todoImageFile{}, ErrInvalid
	}
	read, err := m.ReadTODO(ctx, companyID, projectID, todoID, nil)
	if err != nil {
		return todoImageFile{}, err
	}
	if read.TODO.Content == nil {
		return todoImageFile{}, ErrNotFound
	}
	var attachment *TODOImageAttachment
	for i := range read.TODO.Content.ImageAttachments {
		if read.TODO.Content.ImageAttachments[i].ID == imageID {
			attachment = &read.TODO.Content.ImageAttachments[i]
			break
		}
	}
	if attachment == nil || !validTODOImagePath(attachment.Path) || !strings.HasPrefix(attachment.Path, "todo-reference-images/"+companyID+"/"+projectID+"/"+todoID+"/") || !strings.HasPrefix(attachment.Path[strings.LastIndex(attachment.Path, "/")+1:], strings.TrimPrefix(attachment.Digest, "sha256:")+".") {
		return todoImageFile{}, ErrNotFound
	}
	binding, err := m.ReadRepositoryBinding(ctx, companyID, projectID)
	if err != nil || binding.State != "configured" || m.prototypeGit == nil {
		return todoImageFile{}, ErrUnavailable
	}
	key := binding.RepositoryID + "\x00" + binding.PublicationBranch + "\x00" + attachment.Path + "\x00" + attachment.Digest
	return m.todoImages.load(ctx, key, func() (todoImageFile, error) {
		return m.readTODOImageFromGit(ctx, companyID, projectID, binding, *attachment)
	})
}

func (m *MongoStore) readTODOImageFromGit(ctx context.Context, companyID, projectID string, binding RepositoryBinding, attachment TODOImageAttachment) (todoImageFile, error) {
	revision, err := m.prototypeGit.BranchHead(ctx, binding)
	if err != nil {
		return todoImageFile{}, err
	}
	source, err := m.prototypeGit.OpenRevision(ctx, companyID, projectID, binding, revision)
	if err != nil {
		return todoImageFile{}, err
	}
	defer source.close()
	data, mode, err := source.source.Read(attachment.Path, maxTODOImageBytes)
	if err != nil || mode != ac.ModeRegular || source.source.VerifyStable() != nil {
		return todoImageFile{}, ErrNotFound
	}
	digest, _, contentType, err := todoImageData(data)
	if err != nil || digest != attachment.Digest {
		return todoImageFile{}, ErrConflict
	}
	return todoImageFile{Data: data, ContentType: contentType, Digest: digest}, nil
}
