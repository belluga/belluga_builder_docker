package registry

import (
	"context"
	"encoding/json"
	"errors"
	"unicode/utf8"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
)

const maxInvestigationBytes = 512 << 10

// ReleaseInvestigation is Project-owned planning content, not a TODO or a
// delivery-stage input. The Release identity is also the document identity.
type ReleaseInvestigation struct {
	Title        string                         `json:"title" bson:"title"`
	Introduction string                         `json:"introduction" bson:"introduction"`
	Materials    []ReleaseInvestigationMaterial `json:"materials" bson:"materials"`
}

type ReleaseInvestigationMaterial struct {
	ID           string   `json:"id" bson:"id"`
	Title        string   `json:"title" bson:"title"`
	BodyMarkdown string   `json:"body_markdown" bson:"body_markdown"`
	SourceRefs   []string `json:"source_refs,omitempty" bson:"source_refs,omitempty"`
}

type ReleaseInvestigationSummary struct {
	Title string `json:"title"`
}

type ReleaseInvestigationRead struct {
	CompanyID       string                `json:"company_id"`
	ProjectID       string                `json:"project_id"`
	ReleaseID       string                `json:"release_id"`
	RoadmapRevision int64                 `json:"roadmap_revision"`
	State           string                `json:"state"`
	Document        *ReleaseInvestigation `json:"document,omitempty"`
}

func validateReleaseInvestigation(value *ReleaseInvestigation) error {
	if value == nil || !validName(value.Title) || !utf8.ValidString(value.Introduction) || len(value.Introduction) > 16<<10 || value.Materials == nil || len(value.Materials) > 32 {
		return todoTeach("investigation", "provide a title, introduction up to 16 KiB and 0..32 ordered materials", ErrInvalid)
	}
	seen := make(map[string]bool, len(value.Materials))
	for _, material := range value.Materials {
		if !validID(material.ID) || seen[material.ID] || !validName(material.Title) || !validSummaryField(material.BodyMarkdown, 64<<10) || len(material.SourceRefs) > 16 {
			return todoTeach("investigation.materials", "use unique canonical IDs, titles, nonblank Markdown up to 64 KiB and at most 16 source references per material", ErrInvalid)
		}
		seen[material.ID] = true
		for _, ref := range material.SourceRefs {
			if !validSummaryField(ref, 512) {
				return todoTeach("investigation.materials.source_refs", "use nonblank UTF-8 references up to 512 bytes", ErrInvalid)
			}
		}
	}
	encoded, err := json.Marshal(value)
	if err != nil || len(encoded) > maxInvestigationBytes {
		return todoTeach("investigation", "fit the complete document within 512 KiB", ErrInvalid)
	}
	return nil
}

func (m *MongoStore) ReadReleaseInvestigation(ctx context.Context, companyID, projectID, releaseID string) (ReleaseInvestigationRead, error) {
	if !validID(companyID) || !validID(projectID) || !validID(releaseID) {
		return ReleaseInvestigationRead{}, ErrInvalid
	}
	project, err := m.Project(ctx, companyID, projectID)
	if err != nil {
		return ReleaseInvestigationRead{}, err
	}
	company, err := m.company(ctx, companyID)
	if err != nil {
		return ReleaseInvestigationRead{}, err
	}
	var release roadmapRelease
	err = m.client.Database(company.DatabaseName).Collection(releasesCollection).FindOne(ctx, bson.D{{Key: "project_id", Value: projectID}, {Key: "id", Value: releaseID}}).Decode(&release)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return ReleaseInvestigationRead{}, todoTeach("release_id", "name an existing Release in this Project", ErrNotFound)
	}
	if err != nil {
		return ReleaseInvestigationRead{}, ErrUnavailable
	}
	result := ReleaseInvestigationRead{CompanyID: companyID, ProjectID: projectID, ReleaseID: releaseID, RoadmapRevision: project.RoadmapRevision, State: "absent"}
	if release.Investigation != nil {
		if err := validateReleaseInvestigation(release.Investigation); err != nil {
			return ReleaseInvestigationRead{}, err
		}
		result.State, result.Document = "available", release.Investigation
	}
	return result, nil
}

func ReleaseInvestigationTemplate() map[string]any {
	return map[string]any{
		"schema_version": "builder-release-investigation-v1",
		"identity":       "one optional document per Company/Project/Release",
		"limits":         map[string]int{"title_runes": 160, "introduction_bytes": 16 << 10, "materials": 32, "material_body_bytes": 64 << 10, "document_bytes": maxInvestigationBytes, "source_refs_per_material": 16},
		"write":          map[string]any{"project_id": "project-id", "id": "release-id", "expected_roadmap_revision": 0, "operation_id": "stable-retry-key", "investigation": map[string]any{"title": "Study title", "introduction": "Optional introduction", "materials": []any{map[string]any{"id": "study-id", "title": "Material title", "body_markdown": "Study content", "source_refs": []string{}}}}},
	}
}
