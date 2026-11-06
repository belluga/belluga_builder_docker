package registry

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"

	"go.mongodb.org/mongo-driver/v2/bson"
)

type roadmapEpic struct {
	StorageID   bson.ObjectID `bson:"_id,omitempty"`
	ProjectID   string        `bson:"project_id"`
	ID          string        `bson:"id"`
	Name        string        `bson:"name"`
	Description string        `bson:"description"`
	Position    int           `bson:"position"`
}
type roadmapRelease struct {
	StorageID     bson.ObjectID         `bson:"_id,omitempty"`
	ProjectID     string                `bson:"project_id"`
	ID            string                `bson:"id"`
	EpicID        string                `bson:"epic_id"`
	Name          string                `bson:"name"`
	Description   string                `bson:"description"`
	Position      int                   `bson:"position"`
	Investigation *ReleaseInvestigation `bson:"investigation,omitempty"`
}
type roadmapReleaseView struct {
	ID            string                       `json:"id"`
	Name          string                       `json:"name"`
	Description   string                       `json:"description"`
	TODOs         []roadmapTODOView            `json:"todos"`
	Investigation *ReleaseInvestigationSummary `json:"investigation,omitempty"`
}

// roadmapTODOView preserves the v3 Reader vocabulary and adds only the
// Mongo-owned revision needed to reject stale list-to-detail reads.
type roadmapTODOView struct {
	ID                    string `json:"id"`
	ReleaseID             string `json:"-"`
	Title                 string `json:"title"`
	Path                  string `json:"path,omitempty"`
	Kind                  string `json:"kind"`
	Objective             string `json:"objective"`
	ObjectiveState        string `json:"objective_state"`
	NextStep              string `json:"next_step"`
	NextStepState         string `json:"next_step_state"`
	RecordState           string `json:"record_state"`
	DeliveryStage         string `json:"delivery_stage,omitempty"`
	AuthoredDeliveryStage string `json:"authored_delivery_stage"`
	DeliveryStageState    string `json:"delivery_stage_state"`
	DeliveryStageTeach    string `json:"delivery_stage_teach,omitempty"`
	Blocked               bool   `json:"blocked"`
	QualifierState        string `json:"qualifier_state"`
	WorkState             string `json:"work_state"`
	TODORevision          int64  `json:"todo_revision"`
}
type roadmapEpicView struct {
	ID          string               `json:"id"`
	Name        string               `json:"name"`
	Description string               `json:"description"`
	Releases    []roadmapReleaseView `json:"releases"`
}

func projectRoadmap(projectID string, revision int64, epics []roadmapEpic, releases []roadmapRelease, todos []roadmapTODOView) (json.RawMessage, error) {
	views := make([]roadmapEpicView, 0, len(epics))
	byID := make(map[string]int, len(epics))
	for _, epic := range epics {
		if epic.StorageID.IsZero() || !validID(epic.ID) || epic.ProjectID != projectID || !validName(epic.Name) || epic.Position < 0 {
			return nil, ErrUnavailable
		}
		if _, exists := byID[epic.ID]; exists {
			return nil, ErrUnavailable
		}
		byID[epic.ID] = len(views)
		views = append(views, roadmapEpicView{ID: epic.ID, Name: epic.Name, Description: epic.Description, Releases: []roadmapReleaseView{}})
	}
	seen := make(map[string]bool, len(releases))
	for _, release := range releases {
		index, exists := byID[release.EpicID]
		if !exists || seen[release.ID] || release.StorageID.IsZero() || !validID(release.ID) || release.ProjectID != projectID || !validName(release.Name) || release.Position < 0 {
			return nil, ErrUnavailable
		}
		seen[release.ID] = true
		view := roadmapReleaseView{ID: release.ID, Name: release.Name, Description: release.Description, TODOs: []roadmapTODOView{}}
		if release.Investigation != nil {
			view.Investigation = &ReleaseInvestigationSummary{Title: release.Investigation.Title}
		}
		views[index].Releases = append(views[index].Releases, view)
	}
	releaseViews := make(map[string]*roadmapReleaseView)
	for i := range views {
		for j := range views[i].Releases {
			releaseViews[views[i].Releases[j].ID] = &views[i].Releases[j]
		}
	}
	backlogTODOs := make([]roadmapTODOView, 0)
	for _, todo := range todos {
		if todo.ReleaseID == "" {
			backlogTODOs = append(backlogTODOs, todo)
			continue
		}
		// The source-to-release relation was verified while loading Mongo records;
		// a missing target here indicates a corrupted hierarchy and must fail closed.
		release := releaseViews[todo.ReleaseID]
		if release == nil {
			return nil, ErrUnavailable
		}
		release.TODOs = append(release.TODOs, todo)
	}
	backlog := struct {
		TODOs   []roadmapTODOView `json:"todos"`
		Folders []any             `json:"folders"`
	}{TODOs: backlogTODOs, Folders: []any{}}
	result := struct {
		SchemaVersion   string            `json:"schema_version"`
		ProjectID       string            `json:"project_id"`
		RoadmapRevision int64             `json:"roadmap_revision"`
		FocusRelease    string            `json:"focus_release_id"`
		Epics           []roadmapEpicView `json:"epics"`
		Backlog         any               `json:"backlog"`
		Unclassified    []any             `json:"unclassified"`
		Diagnostics     []any             `json:"diagnostics"`
	}{"builder-roadmap-projection-v3", projectID, revision, "", views, backlog, []any{}, []any{}}
	data, err := json.Marshal(result)
	if err != nil {
		return nil, ErrUnavailable
	}
	return data, nil
}

func roadmapTODOViewFromRecord(record TODORecord) roadmapTODOView {
	contract, selected := selectedTODOContract(record.ID)
	path := record.Source.Path
	if selected && record.Source.Kind != "mongo-content-revision" {
		path = contract.Path
	}
	kind := "executable"
	blocked := containsTODOString(record.Qualifiers, "Blocked")
	workState := "unknown"
	if blocked {
		workState = "blocked"
	}
	view := roadmapTODOView{
		ID: record.ID, ReleaseID: record.ReleaseID, Title: record.Title, Path: path, Kind: kind,
		Objective: record.Objective, ObjectiveState: "complete", NextStep: record.NextExactStep, NextStepState: "complete",
		RecordState: "active", AuthoredDeliveryStage: record.Stage, DeliveryStageState: "unverified",
		Blocked: blocked, QualifierState: "verified", WorkState: workState, TODORevision: record.Revision,
	}
	if record.Source.Kind == "mongo-content-revision" {
		view.Path = ""
	}
	if record.StageVerification == "verified" && record.VerifiedTransitionRef != nil {
		view.DeliveryStage = record.Stage
		view.DeliveryStageState = "verified"
	} else {
		if record.Source.Kind == "mongo-content-revision" && record.Stage == "Planning" {
			view.DeliveryStageTeach = fmt.Sprintf("TEACH: Planning needs current-stage verification. Run builder-project status --company-id <company-id> --project-id %s --todo-id %s to obtain the exact recovery command; no Foundation path is involved.", record.ProjectID, record.ID)
		} else {
			view.DeliveryStageTeach = fmt.Sprintf("TEACH: authored stage %s has no verified current-stage proof; have the private verifier validate the current stage before treating it as effective.", record.Stage)
		}
	}
	return view
}

func roadmapStorageID(projectID, recordID string) string { return projectID + ":" + recordID }

var idPattern = regexp.MustCompile(`^[a-z][a-z0-9-]{0,63}$`)
var digestPattern = regexp.MustCompile(`^sha256:[a-f0-9]{64}$`)
var branchPattern = regexp.MustCompile(`^[A-Za-z0-9._/-]{1,255}$`)

func validID(value string) bool          { return idPattern.MatchString(value) }
func validName(value string) bool        { return strings.TrimSpace(value) != "" && len([]rune(value)) <= 160 }
func validFingerprint(value string) bool { return digestPattern.MatchString(value) }
func validSnapshotFingerprint(value string) bool {
	if len(value) != 64 || strings.ToLower(value) != value {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}
func validSnapshotState(value string) bool {
	return value == "ready" || value == "not_ready" || value == "invalid" || value == "unavailable"
}
func validBinding(value Binding) bool {
	legacyLocator := value.WorkspaceRoot != "" || value.ProjectRoot != "" || value.FoundationRoot != ""
	if value.RepositoryID == "" && value.PublicationBranch == "" {
		if !legacyLocator {
			return value.DockerRepositoryID != "" || value.SourceBindingsPath != "" || value.SourceBindingsDigest != "" || value.DefaultOwnerID != "" || value.SourceBindingsStatePath != ""
		}
	} else if !validGitRepositoryIdentity(value.RepositoryID) || !validGitBranch(value.PublicationBranch) {
		return false
	}
	if legacyLocator && (!strings.HasPrefix(value.WorkspaceRoot, "/") || value.ProjectRoot == "" || value.FoundationRoot == "") {
		return false
	}
	for _, path := range []string{value.ProjectRoot, value.FoundationRoot, value.SourceBindingsPath, value.SourceBindingsStatePath} {
		if path != "" && (strings.HasPrefix(path, "/") || strings.Contains(path, "\\") || strings.Contains(path, "..")) {
			return false
		}
	}
	if value.SourceBindingsDigest != "" && !validFingerprint(value.SourceBindingsDigest) {
		return false
	}
	return true
}

func legacyFoundationSourceEligible(value *Binding) bool {
	return value != nil && value.FoundationRoot != "" && !value.LegacyFoundationSourceDisabled && value.RepositoryID != "" && value.PublicationBranch != ""
}
func tenantKey(kind, identifier string) (string, error) {
	value := strings.ToLower(strings.TrimSpace(identifier))
	switch kind {
	case "host":
		value = strings.TrimSuffix(value, ".")
		if value == "" || strings.ContainsAny(value, ":/@") {
			return "", errors.New("invalid host")
		}
		for _, label := range strings.Split(value, ".") {
			if label == "" || strings.HasPrefix(label, "-") || strings.HasSuffix(label, "-") {
				return "", errors.New("invalid host")
			}
		}
	case "app_android", "app_ios":
		if value == "" || len(value) > 255 || strings.ContainsAny(value, "\x00/\\") {
			return "", errors.New("invalid app identifier")
		}
	default:
		return "", errors.New("invalid tenant identifier type")
	}
	return kind + ":" + value, nil
}
func tenantDatabase(companyID string) string {
	value := "builder_tenant_" + strings.ReplaceAll(companyID, "-", "_")
	if len(value) <= 63 {
		return value
	}
	return "tenant_" + fmt.Sprintf("%x", sha256Bytes([]byte(companyID)))[:56]
}
func sha256Bytes(data []byte) []byte { sum := sha256.Sum256(data); return sum[:] }
