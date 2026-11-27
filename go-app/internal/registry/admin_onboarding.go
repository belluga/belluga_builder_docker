package registry

import (
	"context"
	"errors"
	"net/http"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
)

const maxCompanyImage = 256 << 10
const maxCompanyForm = 272 << 10

type registrationGrant struct {
	Issuer             string `bson:"issuer"`
	Subject            string `bson:"subject"`
	Operation          string `bson:"operation_id"`
	Digest             string `bson:"digest"`
	DiscoveryOperation string `bson:"discovery_operation_id,omitempty"`
}

func (g *registrationGrant) permits(operator githubOperator) bool {
	return g != nil && g.Issuer == operator.Issuer && g.Subject == operator.Subject
}

// The native unique key claims an operation independently of its slug.
func (g *registrationGrant) objectID() bson.ObjectID {
	const prefix = "sha256:"
	digest := githubRequestDigest([]string{g.Issuer, g.Subject, g.Operation})
	id, _ := bson.ObjectIDFromHex(digest[len(prefix) : len(prefix)+24])
	return id
}

type companyAdminImage struct {
	MIME  string `bson:"mime"`
	Bytes []byte `bson:"bytes"`
}

type adminRegistration struct {
	Name               string `json:"name"`
	Slug               string `json:"slug"`
	Operation          string `json:"operation_id"`
	DiscoveryOperation string `json:"discovery_operation_id,omitempty"`
}

func (r adminRegistration) valid() bool {
	return validName(r.Name) && validID(r.Slug) && validID(r.Operation)
}

func (r adminRegistration) grant(operator githubOperator, intent any) *registrationGrant {
	return &registrationGrant{Issuer: operator.Issuer, Subject: operator.Subject, Operation: r.Operation, Digest: githubRequestDigest(intent), DiscoveryOperation: r.DiscoveryOperation}
}

func (m *MongoStore) createAdminCompany(ctx context.Context, operator githubOperator, req adminRegistration, image []byte) (Company, error) {
	if !req.valid() || len(image) > maxCompanyImage {
		return Company{}, ErrInvalid
	}
	item := Company{ID: req.Slug, Name: req.Name, DatabaseName: m.tenantDatabase(req.Slug), SchemaState: "initializing"}
	if len(image) > 0 {
		mime := http.DetectContentType(image)
		switch mime {
		case "image/png", "image/jpeg", "image/webp":
		default:
			return Company{}, ErrInvalid
		}
		item.AdminImage = &companyAdminImage{MIME: mime, Bytes: image}
		item.HasAdminImage = true
	}
	item.CreatedBy = req.grant(operator, struct {
		Registration adminRegistration
		ImageDigest  string
	}{req, sourceHash(image)})
	item.ObjectID = item.CreatedBy.objectID()
	ctx, cancel := context.WithTimeout(ctx, companyRegistrationTimeout)
	defer cancel()
	_, err := m.control.Collection(companiesCollection).InsertOne(ctx, item)
	if err != nil {
		if !mongo.IsDuplicateKeyError(err) {
			return Company{}, normalizeMongoError(err)
		}
		old, err := m.rawCompany(ctx, item.ID)
		if errors.Is(err, ErrNotFound) {
			return Company{}, ErrConflict
		}
		if err != nil {
			return Company{}, err
		}
		if old.CreatedBy == nil || *old.CreatedBy != *item.CreatedBy {
			return Company{}, ErrConflict
		}
	}
	if err := m.convergeCompanySchema(ctx, item.ID, false); err != nil {
		return Company{}, err
	}
	company, err := m.company(ctx, item.ID)
	return sanitizeCompany(company), err
}

func (m *MongoStore) createAdminProject(ctx context.Context, company string, operator githubOperator, req adminRegistration) (Project, error) {
	if !req.valid() || req.DiscoveryOperation != "" && !validID(req.DiscoveryOperation) {
		return Project{}, ErrInvalid
	}
	co, err := m.company(ctx, company)
	if err != nil {
		return Project{}, err
	}
	doc := projectDocument{ID: req.Slug, Name: req.Name, CompanyID: company, RegisteredAt: time.Now().UTC(), PreparationState: "not_ready", CreatedBy: req.grant(operator, req)}
	doc.ObjectID = doc.CreatedBy.objectID()
	_, err = m.client.Database(co.DatabaseName).Collection(projectsCollection).InsertOne(ctx, doc)
	if err != nil {
		if !mongo.IsDuplicateKeyError(err) {
			return Project{}, normalizeMongoError(err)
		}
		old, err := m.readProjectDocument(ctx, co, req.Slug)
		if errors.Is(err, ErrNotFound) {
			return Project{}, ErrConflict
		}
		if err != nil {
			return Project{}, err
		}
		if old.CreatedBy == nil || *old.CreatedBy != *doc.CreatedBy {
			return Project{}, ErrConflict
		}
		return old.toAPI(), nil
	}
	return doc.toAPI(), nil
}

func (m *MongoStore) readAdminCompanyImage(ctx context.Context, company string) (companyAdminImage, error) {
	var item struct {
		Image *companyAdminImage `bson:"admin_image"`
	}
	err := m.control.Collection(companiesCollection).FindOne(ctx, bson.M{"id": company, "schema_state": "ready"}, options.FindOne().SetProjection(bson.M{"admin_image": 1})).Decode(&item)
	if errors.Is(err, mongo.ErrNoDocuments) || err == nil && item.Image == nil {
		return companyAdminImage{}, ErrNotFound
	}
	if err != nil {
		return companyAdminImage{}, normalizeMongoError(err)
	}
	if len(item.Image.Bytes) == 0 || len(item.Image.Bytes) > maxCompanyImage {
		return companyAdminImage{}, ErrUnavailable
	}
	switch mime := http.DetectContentType(item.Image.Bytes); mime {
	case "image/png", "image/jpeg", "image/webp":
		if mime == item.Image.MIME {
			return *item.Image, nil
		}
	}
	return companyAdminImage{}, ErrUnavailable
}
