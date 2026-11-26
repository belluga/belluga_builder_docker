package registry

import (
	"encoding/json"
	"io"
	"net/url"
	"os"
)

type GitHubAppConfig struct {
	AppID, Slug, ClientID, ClientSecret, PrivateKeyFile        string
	AdminOrigin, AccessIssuer, AccessAudience, AccessOperators string
	CompanyCreators                                            string
	SharedAuthenticatedAccess                                  bool
}

// ConfigureGitHubApp is called once at startup before serving requests. An
// entirely absent config disables the admin routes, while a partial config
// rejects startup and cannot fall back to unauthenticated administration.
func (m *MongoStore) ConfigureGitHubApp(config GitHubAppConfig) error {
	if config == (GitHubAppConfig{}) {
		return nil
	}
	origin, err := url.Parse(config.AdminOrigin)
	if err != nil || !githubCanonicalHTTPSOrigin(origin, config.AdminOrigin) || config.PrivateKeyFile == "" {
		return ErrInvalid
	}
	appID, err := githubPositiveID(config.AppID)
	if err != nil {
		return err
	}
	access, err := newGitHubAccessVerifier(config.AccessIssuer, config.AccessAudience, config.AccessOperators, nil)
	if err != nil {
		return err
	}
	file, err := os.Open(config.PrivateKeyFile)
	if err != nil {
		return ErrUnavailable
	}
	defer file.Close()
	const limit = 32 << 10
	key, err := io.ReadAll(io.LimitReader(file, limit+1))
	if err != nil || len(key) > limit {
		return ErrUnavailable
	}
	app, err := newGitHubAppClient(appID, config.Slug, config.ClientID, config.ClientSecret, config.AdminOrigin+"/admin/github/oauth/callback", key, nil)
	if err != nil {
		return err
	}
	creators := make(map[string]bool)
	if config.CompanyCreators != "" {
		var subjects []string
		if len(config.CompanyCreators) > 16<<10 || json.Unmarshal([]byte(config.CompanyCreators), &subjects) != nil || subjects == nil {
			return ErrInvalid
		}
		for _, subject := range subjects {
			if !validSummaryField(subject, 256) || creators[subject] {
				return ErrInvalid
			}
			creators[subject] = true
		}
	}
	if !config.SharedAuthenticatedAccess && len(creators) == 0 && len(access.operators) == 0 {
		return ErrInvalid
	}
	m.githubApp = app
	m.githubAdminHTTP = &githubAdminHTTP{admin: newGitHubConnectionAdmin(m, app), access: access, origin: config.AdminOrigin, store: m, companyCreators: creators, sharedAuthenticatedAccess: config.SharedAuthenticatedAccess}
	return nil
}
