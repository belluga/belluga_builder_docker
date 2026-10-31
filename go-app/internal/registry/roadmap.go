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
	StorageID   bson.ObjectID `bson:"_id,omitempty"`
	ProjectID   string        `bson:"project_id"`
	ID          string        `bson:"id"`
	EpicID      string        `bson:"epic_id"`
	Name        string        `bson:"name"`
	Description string        `bson:"description"`
	Position    int           `bson:"position"`
}
type roadmapReleaseView struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description"`
	TODOs       []any  `json:"todos"`
}
type roadmapEpicView struct {
	ID          string               `json:"id"`
	Name        string               `json:"name"`
	Description string               `json:"description"`
	Releases    []roadmapReleaseView `json:"releases"`
}

func projectRoadmap(projectID string, epics []roadmapEpic, releases []roadmapRelease) (json.RawMessage, error) {
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
		views[index].Releases = append(views[index].Releases, roadmapReleaseView{ID: release.ID, Name: release.Name, Description: release.Description, TODOs: []any{}})
	}
	backlog := struct {
		TODOs   []any `json:"todos"`
		Folders []any `json:"folders"`
	}{TODOs: []any{}, Folders: []any{}}
	result := struct {
		SchemaVersion string            `json:"schema_version"`
		ProjectID     string            `json:"project_id"`
		FocusRelease  string            `json:"focus_release_id"`
		Epics         []roadmapEpicView `json:"epics"`
		Backlog       any               `json:"backlog"`
		Unclassified  []any             `json:"unclassified"`
		Diagnostics   []any             `json:"diagnostics"`
	}{"builder-roadmap-projection-v3", projectID, "", views, backlog, []any{}, []any{}}
	data, err := json.Marshal(result)
	if err != nil {
		return nil, ErrUnavailable
	}
	return data, nil
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
	if !strings.HasPrefix(value.WorkspaceRoot, "/") || value.ProjectRoot == "" || value.FoundationRoot == "" || value.RepositoryID == "" || !branchPattern.MatchString(value.PublicationBranch) {
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
