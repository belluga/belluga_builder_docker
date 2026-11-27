package registry

import (
	"context"
	"errors"
	"net/http"
	"regexp"
	"sort"
	"strings"
	"unicode/utf8"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
)

const adminProjectsPageSize = 32

type adminGlobalProject struct {
	ID        string `json:"id" bson:"id"`
	Name      string `json:"name" bson:"name"`
	CompanyID string `json:"company_id" bson:"-"`
	Company   string `json:"company_name" bson:"-"`
}

type adminProjectsPage struct {
	Projects   []adminGlobalProject `json:"projects"`
	NextCursor string               `json:"next_cursor,omitempty"`
}

func (h *githubAdminHTTP) adminProjects(r *http.Request, t githubAdminTarget) (any, error) {
	query := r.URL.Query()
	for key, values := range query {
		if len(values) != 1 || key != "company_id" && key != "q" && key != "cursor" {
			return nil, ErrInvalid
		}
	}
	companyFilter := query.Get("company_id")
	search := strings.TrimSpace(query.Get("q"))
	afterCompany, afterProject, hasCursor := strings.Cut(query.Get("cursor"), ":")
	if utf8.RuneCountInString(search) > 160 || companyFilter != "" && !validID(companyFilter) ||
		query.Has("cursor") && (!hasCursor || !validID(afterCompany) || !validID(afterProject)) {
		return nil, ErrInvalid
	}

	ctx := r.Context()
	companies, err := h.adminProjectCompanies(ctx, companyFilter)
	if err != nil {
		return nil, err
	}
	sort.Slice(companies, func(i, j int) bool { return companies[i].ID < companies[j].ID })
	projects := make([]adminGlobalProject, 0, adminProjectsPageSize+1)
	for _, company := range companies {
		if hasCursor && company.ID < afterCompany || !h.canReadAdminCompany(company, t.operator) {
			continue
		}
		filter := bson.M{"company_id": company.ID}
		if hasCursor && company.ID == afterCompany {
			filter["id"] = bson.M{"$gt": afterProject}
		}
		if search != "" && !strings.Contains(strings.ToLower(company.Name), strings.ToLower(search)) &&
			!strings.Contains(strings.ToLower(company.ID), strings.ToLower(search)) {
			matcher := bson.Regex{Pattern: regexp.QuoteMeta(search), Options: "i"}
			filter["$or"] = bson.A{bson.M{"name": matcher}, bson.M{"id": matcher}}
		}
		collection := h.store.client.Database(company.DatabaseName).Collection(projectsCollection)
		find := options.Find().SetProjection(bson.M{"id": 1, "name": 1, "_id": 0}).SetSort(bson.D{{Key: "id", Value: 1}}).
			SetLimit(int64(adminProjectsPageSize + 1 - len(projects)))
		cursor, err := collection.Find(ctx, filter, find)
		if err != nil {
			return nil, normalizeMongoError(err)
		}
		var rows []adminGlobalProject
		if err := cursor.All(ctx, &rows); err != nil {
			return nil, normalizeMongoError(err)
		}
		for _, row := range rows {
			row.CompanyID, row.Company = company.ID, company.Name
			projects = append(projects, row)
		}
		if len(projects) > adminProjectsPageSize {
			break
		}
	}
	next := ""
	if len(projects) > adminProjectsPageSize {
		projects = projects[:adminProjectsPageSize]
		last := projects[len(projects)-1]
		next = last.CompanyID + ":" + last.ID
	}
	return adminProjectsPage{Projects: projects, NextCursor: next}, nil
}

func (h *githubAdminHTTP) adminProjectCompanies(ctx context.Context, id string) ([]Company, error) {
	if id == "" {
		return h.store.readCompanies(ctx)
	}
	company, err := h.store.rawCompany(ctx, id)
	if errors.Is(err, ErrNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return []Company{company}, nil
}

func (h *githubAdminHTTP) canReadAdminCompany(company Company, operator githubOperator) bool {
	return h.store.companySchemaReady(company) == nil &&
		(h.sharedAuthenticatedAccess || h.access.permits(operator, company.ID) || company.CreatedBy.permits(operator))
}
