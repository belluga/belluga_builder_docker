package registry

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestDiscoveryRejectsInvalidQueriesBeforeStorage(t *testing.T) {
	handler := NewHandler(&testService{})
	for _, target := range []string{
		"/internal/v1/companies/search",
		"/internal/v1/companies/search?name=Festou&id=festou",
		"/internal/v1/companies/search?name=Festou&company_ids=invalid/id",
		"/internal/v1/companies/search?name=%FF",
		"/internal/v1/companies/search?name=Festou&limit=51",
		"/internal/v1/companies/company-a/projects?name=Festou&name=Festou",
		"/internal/v1/companies/company-a/projects?name=Festou&company_ids=company-b",
		"/internal/v1/companies/company-a/projects?name=Festou&cursor=not-a-cursor",
	} {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, target, nil))
		if response.Code != http.StatusBadRequest || !strings.Contains(response.Body.String(), "teach_detail") {
			t.Errorf("%s: got %d %s", target, response.Code, response.Body)
		}
	}
}
