package registry

import (
	"context"
	"encoding/json"
	"errors"
	"sort"
	"unicode/utf8"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
)

const maxReleaseSequencingPlanBytes = 512 << 10

type ReleaseSequencingPlan struct {
	Title        string                     `json:"title" bson:"title"`
	Introduction string                     `json:"introduction" bson:"introduction"`
	Rows         []ReleaseSequencingPlanRow `json:"rows" bson:"rows"`
}

type ReleaseSequencingPlanRow struct {
	TODOID             string   `json:"todo_id" bson:"todo_id"`
	DependsOn          []string `json:"depends_on" bson:"depends_on"`
	Rationale          string   `json:"rationale" bson:"rationale"`
	Uncertainties      []string `json:"uncertainties" bson:"uncertainties"`
	CheckpointProposal string   `json:"checkpoint_proposal" bson:"checkpoint_proposal"`
}

type ReleaseSequencingMember struct {
	TODOID   string `json:"todo_id"`
	Stage    string `json:"stage"`
	Revision int64  `json:"revision"`
	Position *int   `json:"position,omitempty"`
}

type ReleaseSequencingStaleReference struct {
	TODOID           string `json:"todo_id"`
	Status           string `json:"status"`
	CurrentReleaseID string `json:"current_release_id,omitempty"`
}

type ReleaseSequencingPlanRead struct {
	CompanyID       string                            `json:"company_id"`
	ProjectID       string                            `json:"project_id"`
	ReleaseID       string                            `json:"release_id"`
	RoadmapRevision int64                             `json:"roadmap_revision"`
	State           string                            `json:"state"`
	Document        *ReleaseSequencingPlan            `json:"document,omitempty"`
	CurrentMembers  []ReleaseSequencingMember         `json:"current_membership"`
	StaleReferences []ReleaseSequencingStaleReference `json:"stale_references"`
}

func validateReleaseSequencingPlan(value *ReleaseSequencingPlan) error {
	if value == nil || !validName(value.Title) || !utf8.ValidString(value.Introduction) || len(value.Introduction) > 16<<10 || value.Rows == nil || len(value.Rows) > 500 {
		return todoTeach("sequencing_plan", "provide a title, introduction up to 16 KiB, and 0..500 ordered rows", ErrInvalid)
	}
	seen := make(map[string]int, len(value.Rows))
	for index, row := range value.Rows {
		if !validID(row.TODOID) || row.DependsOn == nil || len(row.DependsOn) > 500 || !validSummaryField(row.Rationale, 2048) || row.Uncertainties == nil || len(row.Uncertainties) > 16 || !validSummaryField(row.CheckpointProposal, 2048) {
			return todoTeach("sequencing_plan.rows", "use unique TODO IDs, nonblank rationale and checkpoint proposals up to 2 KiB, and at most 16 uncertainties per row", ErrInvalid)
		}
		if _, exists := seen[row.TODOID]; exists {
			return todoTeach("sequencing_plan.rows.todo_id", "appear only once in the ordered plan", ErrInvalid)
		}
		seen[row.TODOID] = index + 1
		depSeen := make(map[string]bool, len(row.DependsOn))
		for _, dependency := range row.DependsOn {
			prior, exists := seen[dependency]
			if !validID(dependency) || !exists || prior >= index+1 || depSeen[dependency] {
				return todoTeach("sequencing_plan.rows.depends_on", "reference unique TODO IDs from earlier rows in this plan", ErrInvalid)
			}
			depSeen[dependency] = true
		}
		for _, uncertainty := range row.Uncertainties {
			if !validSummaryField(uncertainty, 2048) {
				return todoTeach("sequencing_plan.rows.uncertainties", "use nonblank UTF-8 entries up to 2 KiB", ErrInvalid)
			}
		}
	}
	encoded, err := json.Marshal(value)
	if err != nil || len(encoded) > maxReleaseSequencingPlanBytes {
		return todoTeach("sequencing_plan", "fit the complete document within 512 KiB", ErrInvalid)
	}
	return nil
}

func (m *MongoStore) ReadReleaseSequencingPlan(ctx context.Context, companyID, projectID, releaseID string) (ReleaseSequencingPlanRead, error) {
	if !validID(companyID) || !validID(projectID) || !validID(releaseID) {
		return ReleaseSequencingPlanRead{}, ErrInvalid
	}
	project, err := m.Project(ctx, companyID, projectID)
	if err != nil {
		return ReleaseSequencingPlanRead{}, err
	}
	company, err := m.company(ctx, companyID)
	if err != nil {
		return ReleaseSequencingPlanRead{}, err
	}
	database := m.client.Database(company.DatabaseName)
	var release roadmapRelease
	err = database.Collection(releasesCollection).FindOne(ctx, bson.D{{Key: "project_id", Value: projectID}, {Key: "id", Value: releaseID}}).Decode(&release)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return ReleaseSequencingPlanRead{}, todoTeach("release_id", "name an existing Release in this Project", ErrNotFound)
	}
	if err != nil || release.ProjectID != projectID || release.ID != releaseID {
		return ReleaseSequencingPlanRead{}, ErrUnavailable
	}
	result := ReleaseSequencingPlanRead{CompanyID: companyID, ProjectID: projectID, ReleaseID: releaseID, RoadmapRevision: project.RoadmapRevision, State: "absent", CurrentMembers: []ReleaseSequencingMember{}, StaleReferences: []ReleaseSequencingStaleReference{}}
	if release.SequencingPlan != nil {
		if err := validateReleaseSequencingPlan(release.SequencingPlan); err != nil {
			return ReleaseSequencingPlanRead{}, err
		}
		result.State, result.Document = "available", release.SequencingPlan
	}
	plannedIDs := make([]string, 0)
	planned := make(map[string]bool)
	if release.SequencingPlan != nil {
		for _, row := range release.SequencingPlan.Rows {
			plannedIDs = append(plannedIDs, row.TODOID)
			planned[row.TODOID] = true
		}
	}
	filter := bson.D{{Key: "project_id", Value: projectID}, {Key: "removed", Value: bson.D{{Key: "$ne", Value: true}}}}
	if len(plannedIDs) > 0 {
		filter = append(filter, bson.E{Key: "$or", Value: bson.A{
			bson.D{{Key: "release_id", Value: releaseID}},
			bson.D{{Key: "id", Value: bson.D{{Key: "$in", Value: plannedIDs}}}},
		}})
	} else {
		filter = append(filter, bson.E{Key: "release_id", Value: releaseID})
	}
	cursor, err := database.Collection(todosCollection).Find(ctx, filter, options.Find().SetProjection(bson.D{{Key: "id", Value: 1}, {Key: "release_id", Value: 1}, {Key: "stage", Value: 1}, {Key: "revision", Value: 1}, {Key: "position", Value: 1}}))
	if err != nil {
		return ReleaseSequencingPlanRead{}, ErrUnavailable
	}
	defer cursor.Close(ctx)
	members := make(map[string]TODORecord)
	selected := make(map[string]TODORecord)
	for cursor.Next(ctx) {
		var todo TODORecord
		if err := cursor.Decode(&todo); err != nil || !validID(todo.ID) {
			return ReleaseSequencingPlanRead{}, ErrUnavailable
		}
		if todo.ReleaseID == releaseID {
			members[todo.ID] = todo
		}
		if planned[todo.ID] {
			selected[todo.ID] = todo
		}
	}
	if err := cursor.Err(); err != nil {
		return ReleaseSequencingPlanRead{}, ErrUnavailable
	}
	for _, todo := range members {
		member := ReleaseSequencingMember{TODOID: todo.ID, Stage: todo.Stage, Revision: todo.Revision, Position: todo.Position}
		result.CurrentMembers = append(result.CurrentMembers, member)
	}
	sort.Slice(result.CurrentMembers, func(i, j int) bool {
		a, b := result.CurrentMembers[i], result.CurrentMembers[j]
		if a.Position != nil && b.Position != nil && *a.Position != *b.Position {
			return *a.Position < *b.Position
		}
		if (a.Position == nil) != (b.Position == nil) {
			return a.Position != nil
		}
		return a.TODOID < b.TODOID
	})
	for _, todoID := range plannedIDs {
		if _, exists := members[todoID]; exists {
			continue
		}
		stale := ReleaseSequencingStaleReference{TODOID: todoID, Status: "missing"}
		if todo, exists := selected[todoID]; exists {
			stale.Status, stale.CurrentReleaseID = "moved", todo.ReleaseID
		}
		result.StaleReferences = append(result.StaleReferences, stale)
	}
	return result, nil
}

func validateReleaseSequencingMembership(ctx context.Context, todos *mongo.Collection, projectID, releaseID string, plan *ReleaseSequencingPlan) error {
	ids := make([]string, len(plan.Rows))
	for i, row := range plan.Rows {
		ids[i] = row.TODOID
	}
	if len(ids) == 0 {
		return nil
	}
	cursor, err := todos.Find(ctx, bson.D{{Key: "project_id", Value: projectID}, {Key: "id", Value: bson.D{{Key: "$in", Value: ids}}}, {Key: "removed", Value: bson.D{{Key: "$ne", Value: true}}}}, options.Find().SetProjection(bson.D{{Key: "id", Value: 1}, {Key: "release_id", Value: 1}}))
	if err != nil {
		return ErrUnavailable
	}
	defer cursor.Close(ctx)
	found := make(map[string]bool, len(ids))
	for cursor.Next(ctx) {
		var record struct {
			ID        string `bson:"id"`
			ReleaseID string `bson:"release_id"`
		}
		if err := cursor.Decode(&record); err != nil {
			return ErrUnavailable
		}
		if record.ReleaseID == releaseID {
			found[record.ID] = true
		}
	}
	if err := cursor.Err(); err != nil {
		return ErrUnavailable
	}
	for _, id := range ids {
		if !found[id] {
			return todoTeach("sequencing_plan.rows.todo_id", "name an active TODO in the same Project and Release", ErrInvalid)
		}
	}
	return nil
}

func ReleaseSequencingPlanTemplate() map[string]any {
	return map[string]any{
		"schema_version": "builder-release-sequencing-plan-v1",
		"identity":       "one optional Draft document per Company/Project/Release; array order is proposed, not canonical TODO order",
		"limits":         map[string]int{"title_runes": 160, "introduction_bytes": 16 << 10, "rows": 500, "text_bytes_per_field": 2048, "uncertainties_per_row": 16, "document_bytes": maxReleaseSequencingPlanBytes},
		"write":          map[string]any{"project_id": "project-id", "id": "release-id", "expected_roadmap_revision": 0, "operation_id": "stable-retry-key", "sequencing_plan": map[string]any{"title": "Draft sequence", "introduction": "Optional context", "rows": []any{map[string]any{"todo_id": "todo-id", "depends_on": []string{}, "rationale": "Why this proposed position", "uncertainties": []string{}, "checkpoint_proposal": "Provisional checkpoint"}}}},
	}
}
