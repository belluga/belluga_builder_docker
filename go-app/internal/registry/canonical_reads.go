package registry

import (
	"context"
	"encoding/json"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
)

func canonicalPending() *canonicalTeachError {
	return &canonicalTeachError{Diagnostic: CanonicalTeachDiagnostic{
		Code: "canonical_pending", FieldPath: "knowledge_generation_id",
		NextAction: "import and validate a typed draft, then explicitly activate this Project's knowledge generation; canonical content is pending",
	}, Cause: ErrNotFound}
}

// A projection carries its owning record's revision; it is never persisted as
// another authority or assigned a separate revision.
type CanonicalProjection struct {
	ProjectID     string             `json:"project_id"`
	GenerationID  string             `json:"generation_id"`
	Family        CanonicalFamily    `json:"family"`
	RecordID      string             `json:"record_id"`
	Revision      int64              `json:"revision"`
	ContentDigest string             `json:"content_digest"`
	Entity        *CanonicalEntity   `json:"entity,omitempty"`
	Decision      *CanonicalDecision `json:"decision,omitempty"`
}

func canonicalProjection(family CanonicalFamily, record CanonicalRecord) CanonicalProjection {
	return CanonicalProjection{ProjectID: record.ProjectID, GenerationID: record.GenerationID, Family: family, RecordID: record.ID, Revision: record.Revision, ContentDigest: record.ContentDigest}
}

func (m *MongoStore) ReadCanonicalEntity(ctx context.Context, companyID, projectID, entityID string) (CanonicalProjection, error) {
	if !canonicalSemanticID.MatchString(entityID) {
		return CanonicalProjection{}, teachCanonical("", "entity_id", "use the exact case-sensitive entity identity", ErrInvalid)
	}
	db, project, err := m.canonicalDB(ctx, companyID, projectID)
	if err != nil {
		return CanonicalProjection{}, err
	}
	if project.KnowledgeGenerationID == "" {
		return CanonicalProjection{}, canonicalPending()
	}
	// A generation has exactly one domain catalog; the identity prefix index
	// bounds this lookup to the Project's catalog, without a global entity store.
	var stored canonicalStored
	err = db.Collection(canonicalDomains).FindOne(ctx, bson.D{{Key: "project_id", Value: projectID}, {Key: "generation_id", Value: project.KnowledgeGenerationID}, {Key: "payload.entities.id", Value: entityID}}).Decode(&stored)
	if err == mongo.ErrNoDocuments {
		return CanonicalProjection{}, ErrNotFound
	}
	if err != nil {
		return CanonicalProjection{}, ErrUnavailable
	}
	record := stored.toRecord()
	var domain DomainCatalogPayload
	if json.Unmarshal(record.Payload, &domain) != nil {
		return CanonicalProjection{}, ErrUnavailable
	}
	for _, entity := range domain.Entities {
		if entity.ID == entityID {
			projection := canonicalProjection(FamilyDomain, record)
			projection.Entity = &entity
			return projection, nil
		}
	}
	return CanonicalProjection{}, ErrNotFound
}

func (m *MongoStore) ReadCanonicalDecision(ctx context.Context, companyID, projectID string, family CanonicalFamily, recordID, decisionID string) (CanonicalProjection, error) {
	if family != FamilyModule && family != FamilyConstitution && family != FamilyPolicy {
		return CanonicalProjection{}, teachCanonical(recordID, "owner_kind", "select module, project_constitution or policy as the decision owner", ErrInvalid)
	}
	if !canonicalSemanticID.MatchString(decisionID) {
		return CanonicalProjection{}, teachCanonical(recordID, "decision_id", "use the exact case-sensitive decision ID within its owner", ErrInvalid)
	}
	record, err := m.ReadCanonical(ctx, companyID, projectID, family, recordID)
	if err != nil {
		return CanonicalProjection{}, err
	}
	var owner struct {
		Decisions []CanonicalDecision `json:"decisions"`
	}
	if json.Unmarshal(record.Payload, &owner) != nil {
		return CanonicalProjection{}, ErrUnavailable
	}
	for _, decision := range owner.Decisions {
		if decision.ID == decisionID {
			projection := canonicalProjection(family, record)
			projection.Decision = &decision
			return projection, nil
		}
	}
	return CanonicalProjection{}, ErrNotFound
}

func (m *MongoStore) ReadCanonicalAsOf(ctx context.Context, companyID, projectID string, family CanonicalFamily, recordID string, revision int64) (CanonicalRecord, error) {
	if revision < 1 || !canonicalID.MatchString(recordID) {
		return CanonicalRecord{}, teachCanonical(recordID, "revision/record_id", "use a positive content revision and stable record ID", ErrInvalid)
	}
	if _, err := canonicalCollection(family); err != nil {
		return CanonicalRecord{}, err
	}
	db, project, err := m.canonicalDB(ctx, companyID, projectID)
	if err != nil {
		return CanonicalRecord{}, err
	}
	if project.KnowledgeGenerationID == "" {
		return CanonicalRecord{}, canonicalPending()
	}
	var version struct {
		Record canonicalStored `bson:"record"`
	}
	err = db.Collection(canonicalVersions).FindOne(ctx, bson.D{{Key: "project_id", Value: projectID}, {Key: "generation_id", Value: project.KnowledgeGenerationID}, {Key: "family", Value: family}, {Key: "record_id", Value: recordID}, {Key: "revision", Value: revision}}).Decode(&version)
	if err == mongo.ErrNoDocuments {
		return CanonicalRecord{}, ErrNotFound
	}
	if err != nil {
		return CanonicalRecord{}, ErrUnavailable
	}
	return version.Record.toRecord(), nil
}

type CanonicalComparison struct {
	From    CanonicalRecord `json:"from"`
	To      CanonicalRecord `json:"to"`
	Changed bool            `json:"changed"`
}

func (m *MongoStore) CompareCanonicalHistory(ctx context.Context, companyID, projectID string, family CanonicalFamily, recordID string, from, to int64) (CanonicalComparison, error) {
	before, err := m.ReadCanonicalAsOf(ctx, companyID, projectID, family, recordID, from)
	if err != nil {
		return CanonicalComparison{}, err
	}
	after, err := m.ReadCanonicalAsOf(ctx, companyID, projectID, family, recordID, to)
	if err != nil {
		return CanonicalComparison{}, err
	}
	return CanonicalComparison{From: before, To: after, Changed: before.ContentDigest != after.ContentDigest}, nil
}
