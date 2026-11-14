package registry

import (
	"context"
	"fmt"
	"strings"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
)

const maxProjectSessions = 100

// Session is a provider conversation permanently bound to one Project.
// ExternalID is provider-owned and is not an authenticated user credential.
type Session struct {
	ID             bson.ObjectID         `json:"id" bson:"_id,omitempty"`
	Type           string                `json:"type" bson:"type"`
	ExternalID     string                `json:"external_id" bson:"external_id"`
	Binding        SessionProjectBinding `json:"binding" bson:"binding"`
	Revision       int64                 `json:"revision" bson:"revision"`
	OwnerReference string                `json:"owner_reference" bson:"owner_reference"`
	Operation      string                `json:"-" bson:"operation_id"`
	Digest         string                `json:"-" bson:"request_digest"`
	CreatedAt      time.Time             `json:"created_at" bson:"created_at"`
}

type SessionProjectBinding struct {
	CompanyID string `json:"company_id" bson:"company_id"`
	ProjectID string `json:"project_id" bson:"project_id"`
}

type SessionBindingRequest struct {
	Type             string `json:"type"`
	ExternalID       string `json:"external_id"`
	CompanyID        string `json:"company_id"`
	ProjectID        string `json:"project_id"`
	ExpectedRevision int64  `json:"expected_revision"`
	OperationID      string `json:"operation_id"`
	OwnerReference   string `json:"owner_reference"`
}

type SessionList struct {
	Items      []Session `json:"items"`
	NextCursor string    `json:"next_cursor,omitempty"`
}

func normalizeSessionIdentity(provider, externalID string) (string, string, error) {
	provider = strings.ToLower(strings.TrimSpace(provider))
	if provider == "" || len(provider) > 32 || strings.Trim(provider, "abcdefghijklmnopqrstuvwxyz0123456789-_") != "" {
		return "", "", fmt.Errorf("%w: session type is invalid", ErrInvalid)
	}
	if externalID == "" || len(externalID) > 512 || strings.TrimSpace(externalID) != externalID {
		return "", "", fmt.Errorf("%w: session external identity is invalid", ErrInvalid)
	}
	return provider, externalID, nil
}

func validateSessionBindingRequest(request SessionBindingRequest) error {
	provider, externalID, err := normalizeSessionIdentity(request.Type, request.ExternalID)
	if err != nil || provider != request.Type || externalID != request.ExternalID || !validID(request.CompanyID) || !validID(request.ProjectID) || request.ExpectedRevision != 0 || !validID(request.OperationID) || strings.TrimSpace(request.OwnerReference) == "" || len(request.OwnerReference) > 512 {
		return fmt.Errorf("%w: session binding requires a provider identity, exact Company/Project, revision zero and operation ID", ErrInvalid)
	}
	return nil
}

// BindSession performs the only binding operation. A persisted binding can
// never be changed; exact retries are accepted by operation ID and payload.
func (m *MongoStore) BindSession(ctx context.Context, request SessionBindingRequest) (Session, error) {
	if err := validateSessionBindingRequest(request); err != nil {
		return Session{}, err
	}
	digest := sourceHash([]byte(request.Type + "\x00" + request.ExternalID + "\x00" + request.CompanyID + "\x00" + request.ProjectID + "\x00" + request.OwnerReference))
	collection := m.control.Collection(sessionsCollection)
	filter := bson.M{"type": request.Type, "external_id": request.ExternalID}
	var existing Session
	err := collection.FindOne(ctx, filter).Decode(&existing)
	if err == nil {
		if existing.Operation == request.OperationID && existing.Digest == digest {
			return existing, nil
		}
		return Session{}, fmt.Errorf("%w: session identity is already bound and immutable", ErrConflict)
	}
	if err != mongo.ErrNoDocuments {
		return Session{}, ErrUnavailable
	}
	if _, err := m.Project(ctx, request.CompanyID, request.ProjectID); err != nil {
		return Session{}, err
	}
	session := Session{ID: bson.NewObjectID(), Type: request.Type, ExternalID: request.ExternalID, Binding: SessionProjectBinding{CompanyID: request.CompanyID, ProjectID: request.ProjectID}, Revision: 1, OwnerReference: request.OwnerReference, Operation: request.OperationID, Digest: digest, CreatedAt: time.Now().UTC()}
	if _, err := collection.InsertOne(ctx, session); err != nil {
		if mongo.IsDuplicateKeyError(err) {
			// A concurrent bind won. Re-read to distinguish an exact retry from
			// an attempted rebind without ever updating the winning document.
			if findErr := collection.FindOne(ctx, filter).Decode(&existing); findErr == nil && existing.Operation == request.OperationID && existing.Digest == digest {
				return existing, nil
			}
			return Session{}, fmt.Errorf("%w: session identity is already bound and immutable", ErrConflict)
		}
		return Session{}, ErrUnavailable
	}
	return session, nil
}

func (m *MongoStore) ReadSession(ctx context.Context, provider, externalID string) (Session, error) {
	provider, externalID, err := normalizeSessionIdentity(provider, externalID)
	if err != nil {
		return Session{}, err
	}
	var session Session
	if err := m.control.Collection(sessionsCollection).FindOne(ctx, bson.M{"type": provider, "external_id": externalID}).Decode(&session); err != nil {
		if err == mongo.ErrNoDocuments {
			return Session{}, ErrNotFound
		}
		return Session{}, ErrUnavailable
	}
	return session, nil
}

// ListProjectSessions returns only the selected Project's sessions and caps
// each page at 100. ObjectId is the stable continuation key.
func (m *MongoStore) ListProjectSessions(ctx context.Context, companyID, projectID string, after bson.ObjectID, limit int) (SessionList, error) {
	if !validID(companyID) || !validID(projectID) || limit < 1 || limit > maxProjectSessions {
		return SessionList{}, fmt.Errorf("%w: session listing requires exact Project identity and a limit from 1 to %d", ErrInvalid, maxProjectSessions)
	}
	filter := bson.M{"binding.company_id": companyID, "binding.project_id": projectID}
	if !after.IsZero() {
		filter["_id"] = bson.M{"$gt": after}
	}
	findOptions := options.Find().SetSort(bson.D{{Key: "_id", Value: 1}}).SetLimit(int64(limit + 1))
	cursor, err := m.control.Collection(sessionsCollection).Find(ctx, filter, findOptions)
	if err != nil {
		return SessionList{}, ErrUnavailable
	}
	defer cursor.Close(ctx)
	items := make([]Session, 0, limit)
	for cursor.Next(ctx) {
		var session Session
		if err := cursor.Decode(&session); err != nil {
			return SessionList{}, ErrUnavailable
		}
		items = append(items, session)
	}
	if err := cursor.Err(); err != nil {
		return SessionList{}, ErrUnavailable
	}
	result := SessionList{Items: items}
	if len(items) > limit {
		result.Items = items[:limit]
		result.NextCursor = result.Items[len(result.Items)-1].ID.Hex()
	}
	return result, nil
}
