package actions

import (
	"context"
	"fmt"
	"net/url"
	"strconv"
	"sync"

	"github.com/go-logr/logr"
)

type MultiClient interface {
	GetClientFor(ctx context.Context, githubConfigURL string, creds ActionsAuth, namespace string) (ActionsService, error)
	GetClientFromSecret(ctx context.Context, githubConfigURL, namespace string, secretData KubernetesSecretData) (ActionsService, error)
}

type multiClient struct {
	// To lock adding and removing of individual clients.
	mu      sync.Mutex
	clients map[ActionsClientKey]*actionsClientWrapper

	logger    logr.Logger
	userAgent string
}

type GitHubAppAuth struct {
	AppID             int64
	AppInstallationID int64
	AppPrivateKey     string
}

type ActionsAuth struct {
	// GitHub App
	AppCreds *GitHubAppAuth

	// GitHub PAT
	Token string
}

type ActionsClientKey struct {
	ActionsURL string
	Auth       ActionsAuth
	Namespace  string
}

type actionsClientWrapper struct {
	// To lock client usage when tokens are being refreshed.
	mu sync.Mutex

	client ActionsService
}

func NewMultiClient(userAgent string, logger logr.Logger) MultiClient {
	return &multiClient{
		mu:        sync.Mutex{},
		clients:   make(map[ActionsClientKey]*actionsClientWrapper),
		logger:    logger,
		userAgent: userAgent,
	}
}

func (m *multiClient) GetClientFor(ctx context.Context, githubConfigURL string, creds ActionsAuth, namespace string) (ActionsService, error) {
	m.logger.Info("retrieve actions client", "githubConfigURL", githubConfigURL, "namespace", namespace)

	parsedGitHubURL, err := url.Parse(githubConfigURL)
	if err != nil {
		return nil, err
	}

	if creds.Token == "" && creds.AppCreds == nil {
		return nil, fmt.Errorf("no credentials provided. either a PAT or GitHub App credentials should be provided")
	}

	if creds.Token != "" && creds.AppCreds != nil {
		return nil, fmt.Errorf("both PAT and GitHub App credentials provided. should only provide one")
	}

	key := ActionsClientKey{
		ActionsURL: parsedGitHubURL.String(),
		Namespace:  namespace,
	}

	if creds.AppCreds != nil {
		key.Auth = ActionsAuth{
			AppCreds: creds.AppCreds,
		}
	}

	if creds.Token != "" {
		key.Auth = ActionsAuth{
			Token: creds.Token,
		}
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	clientWrapper, has := m.clients[key]
	if has {
		m.logger.Info("using cache client", "githubConfigURL", githubConfigURL, "namespace", namespace)
		return clientWrapper.client, nil
	}

	m.logger.Info("creating new client", "githubConfigURL", githubConfigURL, "namespace", namespace)

	client, err := NewClient(ctx, githubConfigURL, &creds, m.userAgent, m.logger)
	if err != nil {
		return nil, err
	}

	m.clients[key] = &actionsClientWrapper{
		mu:     sync.Mutex{},
		client: client,
	}

	m.logger.Info("successfully created new client", "githubConfigURL", githubConfigURL, "namespace", namespace)

	return client, nil
}

type KubernetesSecretData map[string][]byte

func (m *multiClient) GetClientFromSecret(ctx context.Context, githubConfigURL, namespace string, secretData KubernetesSecretData) (ActionsService, error) {
	if len(secretData) == 0 {
		return nil, fmt.Errorf("must provide secret data with either PAT or GitHub App Auth")
	}

	token := string(secretData["github_token"])
	hasToken := len(token) > 0

	appID := string(secretData["github_app_id"])
	appInstallationID := string(secretData["github_app_installation_id"])
	appPrivateKey := string(secretData["github_app_private_key"])
	hasGitHubAppAuth := len(appID) > 0 && len(appInstallationID) > 0 && len(appPrivateKey) > 0

	if hasToken && hasGitHubAppAuth {
		return nil, fmt.Errorf("must provide secret with only PAT or GitHub App Auth to avoid ambiguity in client behavior")
	}

	if !hasToken && !hasGitHubAppAuth {
		return nil, fmt.Errorf("neither PAT nor GitHub App Auth credentials provided in secret")
	}

	auth := ActionsAuth{}

	if hasToken {
		auth.Token = token
		return m.GetClientFor(ctx, githubConfigURL, auth, namespace)
	}

	parsedAppID, err := strconv.ParseInt(appID, 10, 64)
	if err != nil {
		return nil, err
	}

	parsedAppInstallationID, err := strconv.ParseInt(appInstallationID, 10, 64)
	if err != nil {
		return nil, err
	}

	auth.AppCreds = &GitHubAppAuth{AppID: parsedAppID, AppInstallationID: parsedAppInstallationID, AppPrivateKey: appPrivateKey}
	return m.GetClientFor(ctx, githubConfigURL, auth, namespace)
}
