package registry

import (
	"context"
	"errors"
	"math"
	"time"
	"unicode/utf8"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
)

// RoadmapChange is a Project-scoped planning operation. The action is selected
// by the private route, never by a caller-authored body field.
type RoadmapChange struct {
	ProjectID               string                `json:"project_id"`
	ExpectedRoadmapRevision *int64                `json:"expected_roadmap_revision"`
	OperationID             string                `json:"operation_id"`
	ID                      string                `json:"id,omitempty"`
	EpicID                  string                `json:"epic_id,omitempty"`
	Name                    string                `json:"name,omitempty"`
	Description             string                `json:"description,omitempty"`
	OrderedIDs              []string              `json:"ordered_ids,omitempty"`
	Investigation           *ReleaseInvestigation `json:"investigation,omitempty"`
	RepositoryID            string                `json:"repository_id,omitempty"`
	Branch                  string                `json:"branch,omitempty"`
	OwnerReference          string                `json:"owner_reference,omitempty"`
}

type RoadmapChangeResult struct {
	CompanyID       string `json:"company_id"`
	ProjectID       string `json:"project_id"`
	Action          string `json:"action"`
	ID              string `json:"id,omitempty"`
	RepositoryID    string `json:"repository_id,omitempty"`
	Branch          string `json:"branch,omitempty"`
	RoadmapRevision int64  `json:"roadmap_revision"`
	NoOp            bool   `json:"no_op"`
}

type roadmapOperation struct {
	ID            string `bson:"id"`
	Action        string `bson:"action"`
	RequestDigest string `bson:"request_digest"`
	ResultID      string `bson:"result_id"`
	Revision      int64  `bson:"revision"`
}

type releaseBranchOperationResult struct {
	ID            string
	RequestDigest string
}

func findReleaseBranchOperation(ctx context.Context, database *mongo.Database, projectID, operationID string) (releaseBranchOperationResult, bool, error) {
	var release roadmapRelease
	err := database.Collection(releasesCollection).FindOne(ctx, bson.D{{Key: "project_id", Value: projectID}, {Key: "delivery_branches.operation_id", Value: operationID}}).Decode(&release)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return releaseBranchOperationResult{}, false, nil
	}
	if err != nil || release.ProjectID != projectID {
		return releaseBranchOperationResult{}, false, ErrUnavailable
	}
	for _, branch := range release.DeliveryBranches {
		if branch.OperationID == operationID {
			return releaseBranchOperationResult{ID: release.ID, RequestDigest: branch.RequestDigest}, true, nil
		}
	}
	return releaseBranchOperationResult{}, false, ErrUnavailable
}

func confirmReleaseDeliveryBranch(ctx context.Context, database *mongo.Database, projectID string, request RoadmapChange, action string) error {
	var project projectDocument
	if err := database.Collection(projectsCollection).FindOne(ctx, bson.D{{Key: "id", Value: projectID}}).Decode(&project); err != nil {
		if errors.Is(err, mongo.ErrNoDocuments) {
			return todoTeach("project_id", "name a registered Project", ErrNotFound)
		}
		return ErrUnavailable
	}
	confirmed := false
	for _, repositoryID := range project.ImplementationRepositories {
		if repositoryID == request.RepositoryID {
			confirmed = true
			break
		}
	}
	if !confirmed {
		return todoTeach("repository_id", "confirm this exact repository in the Project implementation repository set before assigning its Release branch", ErrConflict)
	}
	releases := database.Collection(releasesCollection)
	filter := bson.D{{Key: "project_id", Value: projectID}, {Key: "id", Value: request.ID}}
	var release roadmapRelease
	if err := releases.FindOne(ctx, filter).Decode(&release); err != nil {
		return missingRoadmapRecord(err, "release_id")
	}
	for _, existing := range release.DeliveryBranches {
		if existing.RepositoryID == request.RepositoryID {
			return todoTeach("branch", "use the immutable branch already confirmed for this Release repository; a different branch requires a new Release", ErrConflict)
		}
	}
	digest, err := todoRequestDigest(struct {
		Action string
		RoadmapChange
	}{action, request})
	if err != nil {
		return ErrInvalid
	}
	entry := releaseDeliveryBranch{
		RepositoryID: request.RepositoryID, Branch: request.Branch,
		OwnerReference: request.OwnerReference, OperationID: request.OperationID,
		RequestDigest: digest, ConfirmedAt: time.Now().UTC(),
	}
	result, err := releases.UpdateOne(ctx, filter, bson.D{{Key: "$push", Value: bson.D{{Key: "delivery_branches", Value: entry}}}})
	if err != nil {
		return normalizeMongoError(err)
	}
	if result.MatchedCount != 1 {
		return todoTeach("release_id", "reload the same-Project Release before confirming its branch", ErrConflict)
	}
	return nil
}

func validRoadmapChange(action string, request RoadmapChange) bool {
	if !validID(request.ProjectID) || request.ExpectedRoadmapRevision == nil || *request.ExpectedRoadmapRevision < 0 || *request.ExpectedRoadmapRevision == math.MaxInt64 || !todoOperationIDPattern.MatchString(request.OperationID) {
		return false
	}
	switch action {
	case "create-epic", "edit-epic", "create-release", "edit-release":
		return validID(request.ID) && validName(request.Name) && utf8.ValidString(request.Description) && len(request.Description) <= 1024 && (action != "create-release" || validID(request.EpicID)) && (action != "edit-release" || request.EpicID == "" || validID(request.EpicID))
	case "delete-epic", "delete-release":
		return validID(request.ID)
	case "confirm-release-branch":
		return validID(request.ID) && validGitRepositoryID(request.RepositoryID) && validDeliveryBranch(request.Branch) && validSummaryField(request.OwnerReference, 512) && request.EpicID == "" && request.Name == "" && request.Description == "" && request.OrderedIDs == nil && request.Investigation == nil
	case "order-epics":
		return request.OrderedIDs != nil && len(request.OrderedIDs) <= 500
	case "order-releases":
		return validID(request.EpicID) && request.OrderedIDs != nil && len(request.OrderedIDs) <= 500
	case "set-investigation":
		return validID(request.ID) && validateReleaseInvestigation(request.Investigation) == nil
	case "clear-investigation":
		return validID(request.ID) && request.Investigation == nil
	default:
		return false
	}
}

func (m *MongoStore) ChangeRoadmap(ctx context.Context, companyID, action string, request RoadmapChange) (RoadmapChangeResult, error) {
	if action == "set-investigation" {
		if err := validateReleaseInvestigation(request.Investigation); err != nil {
			return RoadmapChangeResult{}, err
		}
	}
	if !validID(companyID) || !validRoadmapChange(action, request) {
		return RoadmapChangeResult{}, todoTeach("roadmap", "provide valid Project, expected Roadmap revision, operation ID, and action fields", ErrInvalid)
	}
	digest, err := todoRequestDigest(struct {
		Action string
		RoadmapChange
	}{action, request})
	if err != nil {
		return RoadmapChangeResult{}, ErrInvalid
	}
	database, err := m.todoTenantDatabase(ctx, companyID)
	if err != nil {
		return RoadmapChangeResult{}, err
	}
	session, err := m.client.StartSession()
	if err != nil {
		return RoadmapChangeResult{}, ErrUnavailable
	}
	defer session.EndSession(ctx)
	var outcome RoadmapChangeResult
	_, err = session.WithTransaction(ctx, func(txCtx context.Context) (any, error) {
		var project projectDocument
		filter := bson.D{{Key: "id", Value: request.ProjectID}, {Key: "company_id", Value: companyID}}
		if err := database.Collection(projectsCollection).FindOne(txCtx, filter).Decode(&project); err != nil {
			if errors.Is(err, mongo.ErrNoDocuments) {
				return nil, todoTeach("project_id", "name a registered Project in this Company", ErrNotFound)
			}
			return nil, ErrUnavailable
		}
		if project.ObjectID.IsZero() || project.CompanyID != companyID || project.RoadmapRevision < 0 {
			return nil, ErrUnavailable
		}
		priorBranchResult, hasPriorBranchOperation, lookupErr := findReleaseBranchOperation(txCtx, database, request.ProjectID, request.OperationID)
		if lookupErr != nil {
			return nil, lookupErr
		}
		if hasPriorBranchOperation {
			if action != "confirm-release-branch" || priorBranchResult.ID != request.ID || priorBranchResult.RequestDigest != digest {
				return nil, todoTeach("operation_id", "reuse this Release branch confirmation ID only for the identical branch confirmation; operation IDs cannot be reused by other Roadmap actions", ErrConflict)
			}
			outcome = RoadmapChangeResult{CompanyID: companyID, ProjectID: request.ProjectID, Action: action, ID: request.ID, RepositoryID: request.RepositoryID, Branch: request.Branch, RoadmapRevision: project.RoadmapRevision, NoOp: true}
			return nil, nil
		}
		if prior := project.RoadmapLastOperation; prior != nil && prior.ID == request.OperationID {
			if prior.Action != action || prior.RequestDigest != digest {
				return nil, todoTeach("operation_id", "not be reused for different Roadmap content", ErrConflict)
			}
			outcome = RoadmapChangeResult{CompanyID: companyID, ProjectID: request.ProjectID, Action: action, ID: prior.ResultID, RoadmapRevision: prior.Revision, NoOp: true}
			return nil, nil
		}
		if project.RoadmapRevision != *request.ExpectedRoadmapRevision {
			return nil, todoTeach("expected_roadmap_revision", "reload the current Project Roadmap before retrying a stale planning operation", ErrConflict)
		}
		next := project.RoadmapRevision + 1
		cas := append(bson.D{}, filter...)
		if project.RoadmapRevision == 0 {
			cas = append(cas, bson.E{Key: "$or", Value: bson.A{bson.D{{Key: "roadmap_revision", Value: 0}}, bson.D{{Key: "roadmap_revision", Value: bson.D{{Key: "$exists", Value: false}}}}}})
		} else {
			cas = append(cas, bson.E{Key: "roadmap_revision", Value: project.RoadmapRevision})
		}
		op := roadmapOperation{ID: request.OperationID, Action: action, RequestDigest: digest, ResultID: request.ID, Revision: next}
		result, err := database.Collection(projectsCollection).UpdateOne(txCtx, cas, bson.D{{Key: "$inc", Value: bson.D{{Key: "roadmap_revision", Value: 1}}}, {Key: "$set", Value: bson.D{{Key: "roadmap_last_operation", Value: op}}}})
		if err != nil {
			return nil, normalizeMongoError(err)
		}
		if result.MatchedCount != 1 {
			return nil, todoTeach("expected_roadmap_revision", "reload after a competing planning operation", ErrConflict)
		}
		if err := changeRoadmapCollections(txCtx, database, request.ProjectID, action, request); err != nil {
			return nil, err
		}
		outcome = RoadmapChangeResult{CompanyID: companyID, ProjectID: request.ProjectID, Action: action, ID: request.ID, RepositoryID: request.RepositoryID, Branch: request.Branch, RoadmapRevision: next}
		return nil, nil
	})
	if err != nil {
		if errors.Is(err, ErrInvalid) || errors.Is(err, ErrNotFound) || errors.Is(err, ErrConflict) {
			return RoadmapChangeResult{}, err
		}
		return RoadmapChangeResult{}, normalizeMongoError(err)
	}
	return outcome, nil
}

func changeRoadmapCollections(ctx context.Context, db *mongo.Database, projectID, action string, req RoadmapChange) error {
	epics, releases, todos := db.Collection(epicsCollection), db.Collection(releasesCollection), db.Collection(todosCollection)
	project := bson.D{{Key: "project_id", Value: projectID}}
	identity := func(id string) bson.D { return bson.D{{Key: "project_id", Value: projectID}, {Key: "id", Value: id}} }
	switch action {
	case "create-epic":
		position, err := nextRoadmapPosition(ctx, epics, project)
		if err != nil {
			return err
		}
		_, err = epics.InsertOne(ctx, roadmapEpic{StorageID: bson.NewObjectID(), ProjectID: projectID, ID: req.ID, Name: req.Name, Description: req.Description, Position: position})
		return normalizeRoadmapWrite(err, "id", "choose an unused Epic ID in this Project")
	case "edit-epic":
		return updateRoadmapFields(ctx, epics, identity(req.ID), bson.D{{Key: "name", Value: req.Name}, {Key: "description", Value: req.Description}}, "epic_id")
	case "delete-epic":
		if count, err := releases.CountDocuments(ctx, bson.D{{Key: "project_id", Value: projectID}, {Key: "epic_id", Value: req.ID}}); err != nil {
			return ErrUnavailable
		} else if count != 0 {
			return todoTeach("epic_id", "remove or move child Releases before deleting an Epic", ErrConflict)
		}
		return deleteRoadmapRecord(ctx, epics, identity(req.ID), "epic_id")
	case "order-epics":
		return setRoadmapOrder(ctx, epics, project, req.OrderedIDs)
	case "create-release":
		if err := requireRoadmapRecord(ctx, epics, identity(req.EpicID), "epic_id"); err != nil {
			return err
		}
		position, err := nextRoadmapPosition(ctx, releases, bson.D{{Key: "project_id", Value: projectID}, {Key: "epic_id", Value: req.EpicID}})
		if err != nil {
			return err
		}
		_, err = releases.InsertOne(ctx, roadmapRelease{StorageID: bson.NewObjectID(), ProjectID: projectID, ID: req.ID, EpicID: req.EpicID, Name: req.Name, Description: req.Description, Position: position})
		return normalizeRoadmapWrite(err, "id", "choose an unused Release ID in this Project")
	case "edit-release":
		var current roadmapRelease
		if err := releases.FindOne(ctx, identity(req.ID)).Decode(&current); err != nil {
			return missingRoadmapRecord(err, "release_id")
		}
		target := current.EpicID
		if req.EpicID != "" {
			target = req.EpicID
		}
		if err := requireRoadmapRecord(ctx, epics, identity(target), "epic_id"); err != nil {
			return err
		}
		fields := bson.D{{Key: "name", Value: req.Name}, {Key: "description", Value: req.Description}}
		if target != current.EpicID {
			position, err := nextRoadmapPosition(ctx, releases, bson.D{{Key: "project_id", Value: projectID}, {Key: "epic_id", Value: target}})
			if err != nil {
				return err
			}
			fields = append(fields, bson.E{Key: "epic_id", Value: target}, bson.E{Key: "position", Value: position})
		}
		return updateRoadmapFields(ctx, releases, identity(req.ID), fields, "release_id")
	case "delete-release":
		var current roadmapRelease
		if err := releases.FindOne(ctx, identity(req.ID)).Decode(&current); err != nil {
			return missingRoadmapRecord(err, "release_id")
		}
		if len(current.DeliveryBranches) > 0 {
			return todoTeach("release_id", "retain this Release because confirmed repository branches are immutable and cannot be reset by deleting/recreating it", ErrConflict)
		}
		if count, err := todos.CountDocuments(ctx, bson.D{{Key: "project_id", Value: projectID}, {Key: "release_id", Value: req.ID}, {Key: "removed", Value: bson.D{{Key: "$ne", Value: true}}}}); err != nil {
			return ErrUnavailable
		} else if count != 0 {
			return todoTeach("release_id", "detach or move member TODOs before deleting a Release", ErrConflict)
		}
		return deleteRoadmapRecord(ctx, releases, identity(req.ID), "release_id")
	case "confirm-release-branch":
		return confirmReleaseDeliveryBranch(ctx, db, projectID, req, action)
	case "set-investigation":
		return updateRoadmapFields(ctx, releases, identity(req.ID), bson.D{{Key: "investigation", Value: req.Investigation}}, "release_id")
	case "clear-investigation":
		result, err := releases.UpdateOne(ctx, identity(req.ID), bson.D{{Key: "$unset", Value: bson.D{{Key: "investigation", Value: ""}}}})
		if err != nil {
			return normalizeMongoError(err)
		}
		if result.MatchedCount == 0 {
			return todoTeach("release_id", "name an existing item in the same Project", ErrNotFound)
		}
		return nil
	case "order-releases":
		if err := requireRoadmapRecord(ctx, epics, identity(req.EpicID), "epic_id"); err != nil {
			return err
		}
		return setRoadmapOrder(ctx, releases, bson.D{{Key: "project_id", Value: projectID}, {Key: "epic_id", Value: req.EpicID}}, req.OrderedIDs)
	default:
		return ErrInvalid
	}
}

func nextRoadmapPosition(ctx context.Context, collection *mongo.Collection, filter bson.D) (int, error) {
	var last struct {
		Position int `bson:"position"`
	}
	err := collection.FindOne(ctx, filter, options.FindOne().SetSort(bson.D{{Key: "position", Value: -1}})).Decode(&last)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return 0, nil
	}
	if err != nil || last.Position < 0 || last.Position == math.MaxInt {
		return 0, ErrUnavailable
	}
	return last.Position + 1, nil
}

func requireRoadmapRecord(ctx context.Context, collection *mongo.Collection, filter bson.D, field string) error {
	err := collection.FindOne(ctx, filter).Err()
	return missingRoadmapRecord(err, field)
}

func missingRoadmapRecord(err error, field string) error {
	if errors.Is(err, mongo.ErrNoDocuments) {
		return todoTeach(field, "name an existing item in the same Project", ErrNotFound)
	}
	if err != nil {
		return ErrUnavailable
	}
	return nil
}

func updateRoadmapFields(ctx context.Context, collection *mongo.Collection, filter, fields bson.D, field string) error {
	result, err := collection.UpdateOne(ctx, filter, bson.D{{Key: "$set", Value: fields}})
	if err != nil {
		return normalizeMongoError(err)
	}
	if result.MatchedCount == 0 {
		return todoTeach(field, "name an existing item in the same Project", ErrNotFound)
	}
	return nil
}

func deleteRoadmapRecord(ctx context.Context, collection *mongo.Collection, filter bson.D, field string) error {
	result, err := collection.DeleteOne(ctx, filter)
	if err != nil {
		return normalizeMongoError(err)
	}
	if result.DeletedCount == 0 {
		return todoTeach(field, "name an existing item in the same Project", ErrNotFound)
	}
	return nil
}

func normalizeRoadmapWrite(err error, field, guidance string) error {
	if err == nil {
		return nil
	}
	if errors.Is(normalizeMongoError(err), ErrConflict) {
		return todoTeach(field, guidance, ErrConflict)
	}
	return ErrUnavailable
}

func setRoadmapOrder(ctx context.Context, collection *mongo.Collection, filter bson.D, ordered []string) error {
	cursor, err := collection.Find(ctx, filter)
	if err != nil {
		return ErrUnavailable
	}
	defer cursor.Close(ctx)
	current := map[string]bool{}
	for cursor.Next(ctx) {
		var item struct {
			ID string `bson:"id"`
		}
		if cursor.Decode(&item) != nil || !validID(item.ID) || current[item.ID] {
			return ErrUnavailable
		}
		current[item.ID] = true
	}
	if cursor.Err() != nil {
		return ErrUnavailable
	}
	if len(current) != len(ordered) {
		return todoTeach("ordered_ids", "supply one exact permutation of the selected Project items", ErrConflict)
	}
	seen := map[string]bool{}
	for position, id := range ordered {
		if !current[id] || seen[id] {
			return todoTeach("ordered_ids", "supply each selected Project item exactly once", ErrConflict)
		}
		seen[id] = true
		itemFilter := append(append(bson.D{}, filter...), bson.E{Key: "id", Value: id})
		if _, err := collection.UpdateOne(ctx, itemFilter, bson.D{{Key: "$set", Value: bson.D{{Key: "position", Value: position}}}}); err != nil {
			return normalizeMongoError(err)
		}
	}
	return nil
}

func bumpRoadmapRevision(ctx context.Context, db *mongo.Database, companyID, projectID string, expected int64) error {
	filter := bson.D{{Key: "id", Value: projectID}, {Key: "company_id", Value: companyID}}
	if expected == 0 {
		filter = append(filter, bson.E{Key: "$or", Value: bson.A{bson.D{{Key: "roadmap_revision", Value: 0}}, bson.D{{Key: "roadmap_revision", Value: bson.D{{Key: "$exists", Value: false}}}}}})
	} else {
		filter = append(filter, bson.E{Key: "roadmap_revision", Value: expected})
	}
	result, err := db.Collection(projectsCollection).UpdateOne(ctx, filter, bson.D{{Key: "$inc", Value: bson.D{{Key: "roadmap_revision", Value: 1}}}, {Key: "$unset", Value: bson.D{{Key: "roadmap_last_operation", Value: ""}}}})
	if err != nil {
		return normalizeMongoError(err)
	}
	if result.MatchedCount != 1 {
		return todoTeach("expected_roadmap_revision", "reload the Project Roadmap after a competing mutation", ErrConflict)
	}
	return nil
}
