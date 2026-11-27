package registry

import (
	"bytes"
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const (
	githubAppAPI           = "https://api.github.com"
	githubAppWeb           = "https://github.com"
	githubAppResponseLimit = 4 << 20
)

var errGitHubCreationUncertain = errors.New("GitHub repository creation result is uncertain")
var errGitHubAccessDenied = errors.New("GitHub App access is not authorized")

type githubAppClient struct {
	http                                   *http.Client
	now                                    func() time.Time
	appID                                  int64
	slug, clientID, clientSecret, callback string
	key                                    *rsa.PrivateKey
}

func newGitHubAppClient(appID int64, slug, clientID, clientSecret, callback string, keyPEM []byte, client *http.Client) (*githubAppClient, error) {
	callbackURL, err := url.Parse(callback)
	if err != nil || callbackURL.Scheme != "https" || callbackURL.Hostname() == "" || callbackURL.User != nil || callbackURL.RawQuery != "" || callbackURL.ForceQuery || callbackURL.Fragment != "" || callbackURL.Path != "/admin/github/oauth/callback" || callbackURL.RawPath != "" ||
		appID <= 0 || !validID(slug) || clientID == "" || clientSecret == "" {
		return nil, ErrInvalid
	}
	block, trailing := pem.Decode(keyPEM)
	if block == nil || len(bytes.TrimSpace(trailing)) != 0 {
		return nil, ErrInvalid
	}
	var key *rsa.PrivateKey
	switch block.Type {
	case "RSA PRIVATE KEY":
		key, err = x509.ParsePKCS1PrivateKey(block.Bytes)
	case "PRIVATE KEY":
		var parsed any
		parsed, err = x509.ParsePKCS8PrivateKey(block.Bytes)
		key, _ = parsed.(*rsa.PrivateKey)
	default:
		return nil, ErrInvalid
	}
	if err != nil || key == nil || key.N.BitLen() < 2048 {
		return nil, ErrInvalid
	}
	if client == nil {
		client = http.DefaultClient
	}
	httpClient := *client
	httpClient.Jar = nil
	httpClient.Timeout = 15 * time.Second
	httpClient.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &githubAppClient{http: &httpClient, now: time.Now, appID: appID, slug: slug, clientID: clientID, clientSecret: clientSecret, callback: callback, key: key}, nil
}

func (c *githubAppClient) appJWT() (string, error) {
	now := c.now()
	claims, err := json.Marshal(struct {
		Issuer    string `json:"iss"`
		IssuedAt  int64  `json:"iat"`
		ExpiresAt int64  `json:"exp"`
	}{strconv.FormatInt(c.appID, 10), now.Add(-time.Minute).Unix(), now.Add(8 * time.Minute).Unix()})
	if err != nil {
		return "", ErrUnavailable
	}
	input := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"RS256","typ":"JWT"}`)) + "." + base64.RawURLEncoding.EncodeToString(claims)
	hash := sha256.Sum256([]byte(input))
	sig, err := rsa.SignPKCS1v15(rand.Reader, c.key, crypto.SHA256, hash[:])
	if err != nil {
		return "", ErrUnavailable
	}
	return input + "." + base64.RawURLEncoding.EncodeToString(sig), nil
}

func (c *githubAppClient) setupURL(state string) string {
	return githubAppWeb + "/apps/" + c.slug + "/installations/new?" + (url.Values{"state": {state}}).Encode()
}

func (c *githubAppClient) authorizationURL(state, verifier string) string {
	hash := sha256.Sum256([]byte(verifier))
	return githubAppWeb + "/login/oauth/authorize?" + (url.Values{
		"client_id": {c.clientID}, "redirect_uri": {c.callback}, "state": {state},
		"code_challenge": {base64.RawURLEncoding.EncodeToString(hash[:])}, "code_challenge_method": {"S256"},
	}).Encode()
}

type githubAppToken struct {
	value     string
	expiresAt time.Time
}

func (c *githubAppClient) exchangeUserCode(ctx context.Context, code, verifier string) (githubAppToken, error) {
	if code == "" || verifier == "" {
		return githubAppToken{}, ErrInvalid
	}
	form := url.Values{"client_id": {c.clientID}, "client_secret": {c.clientSecret}, "code": {code}, "redirect_uri": {c.callback}, "code_verifier": {verifier}}
	var response struct {
		Token     string `json:"access_token"`
		ExpiresIn int64  `json:"expires_in"`
		Error     string `json:"error"`
	}
	status, err := c.request(ctx, http.MethodPost, githubAppWeb+"/login/oauth/access_token", "", "application/x-www-form-urlencoded", strings.NewReader(form.Encode()), &response)
	if err != nil {
		return githubAppToken{}, err
	}
	if response.Error != "" || !validGitHubAppBearer(response.Token) {
		return githubAppToken{}, errGitHubAccessDenied
	}
	if response.ExpiresIn <= 0 || response.ExpiresIn > 86400 {
		return githubAppToken{}, ErrUnavailable
	}
	if status != http.StatusOK {
		return githubAppToken{}, ErrUnavailable
	}
	return githubAppToken{value: response.Token, expiresAt: c.now().Add(time.Duration(response.ExpiresIn) * time.Second)}, nil
}

type githubAppAccount struct {
	ID    int64  `json:"id"`
	Login string `json:"login"`
	Type  string `json:"type"`
}

type githubAppInstallation struct {
	ID          int64            `json:"id"`
	AppID       int64            `json:"app_id"`
	Account     githubAppAccount `json:"account"`
	SuspendedAt *time.Time       `json:"suspended_at"`
}

type githubAppRepository struct {
	ID            int64            `json:"id"`
	NodeID        string           `json:"node_id"`
	FullName      string           `json:"full_name"`
	DefaultBranch string           `json:"default_branch"`
	Private       bool             `json:"private"`
	Owner         githubAppAccount `json:"owner"`
}

func (r githubAppRepository) validIdentity() bool {
	return r.ID > 0 && r.NodeID != "" && len(r.NodeID) <= 128 && validGitRepositoryID(r.FullName) && r.Owner.ID > 0
}

func (c *githubAppClient) user(ctx context.Context, token githubAppToken) (githubAppAccount, error) {
	var user githubAppAccount
	err := c.userGET(ctx, token, "/user", &user)
	if err == nil && user.ID <= 0 {
		err = ErrUnavailable
	}
	return user, err
}

func (c *githubAppClient) userInstallations(ctx context.Context, token githubAppToken, visit func(githubAppInstallation) bool) error {
	for page := 1; page <= 100; page++ {
		var response struct {
			Installations []githubAppInstallation `json:"installations"`
		}
		if err := c.userGET(ctx, token, githubPagedPath("/user/installations", page), &response); err != nil {
			return err
		}
		for _, installation := range response.Installations {
			if installation.ID > 0 && installation.AppID == c.appID && installation.Account.ID > 0 &&
				installation.SuspendedAt == nil && visit(installation) {
				return nil
			}
		}
		if len(response.Installations) < 100 {
			return nil
		}
	}
	return ErrUnavailable
}

func (c *githubAppClient) userInstallation(ctx context.Context, token githubAppToken, installationID, accountID int64) (githubAppInstallation, error) {
	var selected githubAppInstallation
	if installationID <= 0 || accountID < 0 {
		return selected, ErrInvalid
	}
	err := c.userInstallations(ctx, token, func(installation githubAppInstallation) bool {
		if installation.ID != installationID || accountID != 0 && installation.Account.ID != accountID {
			return false
		}
		selected = installation
		return true
	})
	if err == nil && selected.ID == 0 {
		err = errGitHubAccessDenied
	}
	return selected, err
}

func (c *githubAppClient) userInstallationRepositoryPage(ctx context.Context, token githubAppToken, installationID int64, page int) ([]githubAppRepository, bool, error) {
	if installationID <= 0 || page < 1 || page > 100 {
		return nil, false, ErrInvalid
	}
	var response struct {
		Repositories []githubAppRepository `json:"repositories"`
	}
	err := c.userGET(ctx, token, githubPagedPath("/user/installations/"+strconv.FormatInt(installationID, 10)+"/repositories", page), &response)
	return response.Repositories, len(response.Repositories) == 100, err
}

func (c *githubAppClient) userInstallationRepository(ctx context.Context, token githubAppToken, installationID, accountID, repositoryID int64) (githubAppRepository, error) {
	if accountID <= 0 || repositoryID <= 0 {
		return githubAppRepository{}, ErrInvalid
	}
	for page := 1; page <= 100; page++ {
		repositories, more, err := c.userInstallationRepositoryPage(ctx, token, installationID, page)
		if err != nil {
			return githubAppRepository{}, err
		}
		for _, repository := range repositories {
			if repository.ID == repositoryID {
				if !repository.validIdentity() || repository.Owner.ID != accountID {
					return githubAppRepository{}, errGitHubAccessDenied
				}
				return repository, nil
			}
		}
		if !more {
			return githubAppRepository{}, errGitHubAccessDenied
		}
	}
	return githubAppRepository{}, ErrUnavailable
}

func (c *githubAppClient) userGET(ctx context.Context, token githubAppToken, path string, target any) error {
	if !c.validUserToken(token) {
		return errGitHubAccessDenied
	}
	_, err := c.request(ctx, http.MethodGet, githubAppAPI+path, token.value, "", nil, target)
	return err
}

func (c *githubAppClient) validUserToken(token githubAppToken) bool {
	return validGitHubAppBearer(token.value) && token.expiresAt.After(c.now())
}

func githubPagedPath(path string, page int) string {
	return path + "?per_page=100&page=" + strconv.Itoa(page)
}

// repositoryReadToken mints a credential for one immutable repository and
// verifies the exact repository identity using that credential before returning it.
func (c *githubAppClient) repositoryReadToken(ctx context.Context, connection githubRepositoryConnection) (githubAppToken, githubAppRepository, error) {
	if connection.InstallationID <= 0 || connection.GitHubRepositoryID <= 0 || connection.AccountID <= 0 || connection.RepositoryNodeID == "" {
		return githubAppToken{}, githubAppRepository{}, ErrInvalid
	}
	signed, err := c.appJWT()
	if err != nil {
		return githubAppToken{}, githubAppRepository{}, err
	}
	payload := struct {
		RepositoryIDs []int64           `json:"repository_ids"`
		Permissions   map[string]string `json:"permissions"`
	}{[]int64{connection.GitHubRepositoryID}, map[string]string{"contents": "read"}}
	var response struct {
		Token     string    `json:"token"`
		ExpiresAt time.Time `json:"expires_at"`
	}
	status, err := c.apiJSON(ctx, http.MethodPost, "/app/installations/"+strconv.FormatInt(connection.InstallationID, 10)+"/access_tokens", signed, payload, &response)
	if err != nil {
		if status == http.StatusUnprocessableEntity {
			// This validated scope can no longer be granted for the installation.
			return githubAppToken{}, githubAppRepository{}, errGitHubAccessDenied
		}
		return githubAppToken{}, githubAppRepository{}, err
	}
	if status != http.StatusCreated || !validGitHubAppBearer(response.Token) || !response.ExpiresAt.After(c.now()) {
		return githubAppToken{}, githubAppRepository{}, ErrUnavailable
	}
	token := githubAppToken{value: response.Token, expiresAt: response.ExpiresAt}
	var repo githubAppRepository
	_, err = c.apiJSON(ctx, http.MethodGet, "/repositories/"+strconv.FormatInt(connection.GitHubRepositoryID, 10), token.value, nil, &repo)
	if err != nil {
		return githubAppToken{}, githubAppRepository{}, err
	}
	if !repo.validIdentity() || repo.ID != connection.GitHubRepositoryID || repo.NodeID != connection.RepositoryNodeID || repo.Owner.ID != connection.AccountID {
		return githubAppToken{}, githubAppRepository{}, errGitHubAccessDenied
	}
	return token, repo, nil
}

func (c *githubAppClient) createOrganizationRepository(ctx context.Context, token githubAppToken, installation githubAppInstallation, name string, private bool) (githubAppRepository, error) {
	account := installation.Account
	if !c.validUserToken(token) || installation.ID <= 0 || installation.AppID != c.appID || installation.SuspendedAt != nil || account.ID <= 0 || account.Type != "Organization" || !validGitRepositoryID(account.Login+"/"+name) || strings.Contains(name, "/") {
		return githubAppRepository{}, ErrInvalid
	}
	payload := struct {
		Name     string `json:"name"`
		Private  bool   `json:"private"`
		AutoInit bool   `json:"auto_init"`
	}{name, private, true}
	var receipt struct {
		githubAppRepository
		Private *bool `json:"private"`
	}
	status, err := c.apiJSON(ctx, http.MethodPost, "/orgs/"+url.PathEscape(account.Login)+"/repos", token.value, payload, &receipt)
	if err != nil {
		switch status {
		case http.StatusBadRequest, http.StatusUnauthorized, http.StatusForbidden, http.StatusNotFound, http.StatusConflict, http.StatusUnprocessableEntity, http.StatusTooManyRequests:
			return githubAppRepository{}, err
		default:
			return githubAppRepository{}, errGitHubCreationUncertain
		}
	}
	repo := receipt.githubAppRepository
	if status != http.StatusCreated || receipt.Private == nil || !repo.validIdentity() || repo.Owner.ID != account.ID || *receipt.Private != private || !strings.EqualFold(repo.FullName, account.Login+"/"+name) || !validGitBranch(repo.DefaultBranch) {
		return githubAppRepository{}, errGitHubCreationUncertain
	}
	repo.Private = *receipt.Private
	return repo, nil
}

func (c *githubAppClient) apiJSON(ctx context.Context, method, path, token string, payload, response any) (int, error) {
	var body io.Reader
	if payload != nil {
		data, err := json.Marshal(payload)
		if err != nil {
			return 0, ErrInvalid
		}
		body = bytes.NewReader(data)
	}
	return c.request(ctx, method, githubAppAPI+path, token, "application/json", body, response)
}

func (c *githubAppClient) request(ctx context.Context, method, endpoint, token, contentType string, body io.Reader, target any) (int, error) {
	request, err := http.NewRequestWithContext(ctx, method, endpoint, body)
	if err != nil {
		return 0, ErrInvalid
	}
	if body != nil {
		request.Header.Set("Content-Type", contentType)
	}
	request.Header.Set("Accept", "application/json")
	if request.URL.Host == "api.github.com" {
		request.Header.Set("Accept", "application/vnd.github+json")
		request.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	}
	if token != "" {
		if !validGitHubAppBearer(token) {
			return 0, ErrInvalid
		}
		request.Header.Set("Authorization", "Bearer "+token)
	}
	response, err := c.http.Do(request)
	if err != nil {
		if ctx.Err() != nil {
			return 0, ctx.Err()
		}
		return 0, ErrUnavailable
	}
	defer response.Body.Close()
	status := response.StatusCode
	if status < 200 || status >= 300 {
		switch status {
		case http.StatusUnauthorized, http.StatusForbidden, http.StatusNotFound:
			if response.Header.Get("X-RateLimit-Remaining") != "0" && response.Header.Get("Retry-After") == "" {
				return status, errGitHubAccessDenied
			}
		case http.StatusBadRequest, http.StatusUnprocessableEntity:
			return status, ErrInvalid
		case http.StatusConflict:
			return status, ErrConflict
		}
		return status, ErrUnavailable
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, githubAppResponseLimit+1))
	if err != nil || len(data) > githubAppResponseLimit || len(bytes.TrimSpace(data)) == 0 || bytes.Equal(bytes.TrimSpace(data), []byte("null")) || json.Unmarshal(data, target) != nil {
		return status, ErrUnavailable
	}
	return status, nil
}

func validGitHubAppBearer(value string) bool {
	if len(value) == 0 || len(value) > 8192 {
		return false
	}
	for _, b := range []byte(value) {
		if b < 0x21 || b > 0x7e {
			return false
		}
	}
	return true
}
