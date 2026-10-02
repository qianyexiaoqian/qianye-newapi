package controller

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/oauth"
	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

type authFlowTestOAuthProvider struct {
	exchangeErr   error
	userInfoErr   error
	exchangeCalls int
	userInfoCalls int
}

func (*authFlowTestOAuthProvider) GetName() string { return "Auth Flow Test" }
func (*authFlowTestOAuthProvider) IsEnabled() bool { return true }
func (provider *authFlowTestOAuthProvider) ExchangeToken(context.Context, string, *gin.Context) (*oauth.OAuthToken, error) {
	provider.exchangeCalls++
	if provider.exchangeErr != nil {
		return nil, provider.exchangeErr
	}
	return &oauth.OAuthToken{}, nil
}
func (provider *authFlowTestOAuthProvider) GetUserInfo(context.Context, *oauth.OAuthToken) (*oauth.OAuthUser, error) {
	provider.userInfoCalls++
	if provider.userInfoErr != nil {
		return nil, provider.userInfoErr
	}
	return &oauth.OAuthUser{ProviderUserID: "external-user"}, nil
}
func (*authFlowTestOAuthProvider) IsUserIDTaken(string) bool                      { return false }
func (*authFlowTestOAuthProvider) FillUserByProviderID(*model.User, string) error { return nil }
func (*authFlowTestOAuthProvider) SetProviderUserID(*model.User, string)          {}
func (*authFlowTestOAuthProvider) GetProviderPrefix() string                      { return "flow_" }
func (*authFlowTestOAuthProvider) ProviderUserIDColumn() string                   { return "" }

func setupAuthFlowControllerTest(t *testing.T) *authFlowTestOAuthProvider {
	t.Helper()
	previousDB := model.DB
	previousType := common.MainDatabaseType()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&model.AuthFlow{}))
	model.DB = db
	common.SetMainDatabaseType(common.DatabaseTypeSQLite)
	provider := &authFlowTestOAuthProvider{}
	oauth.Register("auth-flow-test", provider)
	t.Cleanup(func() {
		oauth.Unregister("auth-flow-test")
		model.DB = previousDB
		common.SetMainDatabaseType(previousType)
	})
	return provider
}

func TestGenerateOAuthCodeCarriesAffiliateInLoginFlow(t *testing.T) {
	setupAuthFlowControllerTest(t)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/api/oauth/state", strings.NewReader(`{"provider":"auth-flow-test","intent":"login","aff":"invite-code"}`))
	c.Request.Header.Set("Content-Type", "application/json")

	GenerateOAuthCode(c)

	require.Equal(t, http.StatusOK, recorder.Code)
	var response struct {
		Success bool `json:"success"`
		Data    struct {
			FlowToken string `json:"flow_token"`
		} `json:"data"`
	}
	require.NoError(t, common.Unmarshal(recorder.Body.Bytes(), &response))
	require.True(t, response.Success)
	flow, err := model.GetAuthFlow(response.Data.FlowToken, model.AuthFlowMatch{
		Purpose: model.AuthFlowPurposeOAuth, Provider: "auth-flow-test", Intent: model.AuthFlowIntentLogin,
	})
	require.NoError(t, err)
	var payload oauthFlowPayload
	require.NoError(t, common.UnmarshalJsonStr(flow.Payload, &payload))
	assert.Equal(t, "invite-code", payload.AffiliateCode)
	assert.Zero(t, flow.UserId)
	assert.Empty(t, flow.SessionId)
}

func TestGenerateOAuthCodeBindsFlowToAuthenticatedSession(t *testing.T) {
	setupAuthFlowControllerTest(t)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/api/oauth/state", strings.NewReader(`{"provider":"auth-flow-test","intent":"bind"}`))
	c.Request.Header.Set("Content-Type", "application/json")
	c.Set("id", 42)
	c.Set("session_id", "session-42")
	c.Set("auth_version", int64(3))
	c.Set("session_version", int64(2))

	GenerateOAuthCode(c)

	require.Equal(t, http.StatusOK, recorder.Code)
	var response struct {
		Success bool `json:"success"`
		Data    struct {
			FlowToken string `json:"flow_token"`
		} `json:"data"`
	}
	require.NoError(t, common.Unmarshal(recorder.Body.Bytes(), &response))
	require.True(t, response.Success)
	flow, err := model.GetAuthFlow(response.Data.FlowToken, model.AuthFlowMatch{
		Purpose: model.AuthFlowPurposeOAuth, Provider: "auth-flow-test", Intent: model.AuthFlowIntentBind,
		UserId: 42, SessionId: "session-42",
	})
	require.NoError(t, err)
	assert.Equal(t, 42, flow.UserId)
	assert.Equal(t, "session-42", flow.SessionId)
}

func TestOAuthLoginConsumesFlowOnlyAfterProviderIdentity(t *testing.T) {
	provider := setupAuthFlowControllerTest(t)

	tests := []struct {
		name        string
		exchangeErr error
		userInfoErr error
	}{
		{name: "exchange failure", exchangeErr: errors.New("exchange failed")},
		{name: "user info failure", userInfoErr: errors.New("user info failed")},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			provider.exchangeErr = test.exchangeErr
			provider.userInfoErr = test.userInfoErr
			token, _, err := model.CreateAuthFlow(model.AuthFlowCreate{
				Purpose: model.AuthFlowPurposeOAuth, Provider: "auth-flow-test", Intent: model.AuthFlowIntentLogin,
				Payload: `{}`, ExpiresAt: time.Now().Add(time.Minute),
			})
			require.NoError(t, err)

			router := gin.New()
			router.GET("/api/oauth/:provider", HandleOAuth)
			request := httptest.NewRequest(http.MethodGet, "/api/oauth/auth-flow-test?state="+token+"&code=test", nil)
			response := httptest.NewRecorder()
			router.ServeHTTP(response, request)

			flow, err := model.GetAuthFlow(token, model.AuthFlowMatch{
				Purpose: model.AuthFlowPurposeOAuth, Provider: "auth-flow-test", Intent: model.AuthFlowIntentLogin,
			})
			require.NoError(t, err)
			assert.Nil(t, flow.ConsumedAt)
		})
	}
}

func TestOAuthLoginConsumesFlowAfterProviderIdentityAndOnProviderError(t *testing.T) {
	provider := setupAuthFlowControllerTest(t)

	provider.exchangeErr = nil
	provider.userInfoErr = nil
	successToken, _, err := model.CreateAuthFlow(model.AuthFlowCreate{
		Purpose: model.AuthFlowPurposeOAuth, Provider: "auth-flow-test", Intent: model.AuthFlowIntentLogin,
		Payload: `{invalid`, ExpiresAt: time.Now().Add(time.Minute),
	})
	require.NoError(t, err)
	router := gin.New()
	router.GET("/api/oauth/:provider", HandleOAuth)
	request := httptest.NewRequest(http.MethodGet, "/api/oauth/auth-flow-test?state="+successToken+"&code=test", nil)
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	_, err = model.GetAuthFlow(successToken, model.AuthFlowMatch{Purpose: model.AuthFlowPurposeOAuth})
	assert.ErrorIs(t, err, model.ErrAuthFlowConsumed)
	assert.Equal(t, 1, provider.exchangeCalls)
	assert.Equal(t, 1, provider.userInfoCalls)

	providerErrorToken, _, err := model.CreateAuthFlow(model.AuthFlowCreate{
		Purpose: model.AuthFlowPurposeOAuth, Provider: "auth-flow-test", Intent: model.AuthFlowIntentLogin,
		Payload: `{}`, ExpiresAt: time.Now().Add(time.Minute),
	})
	require.NoError(t, err)
	request = httptest.NewRequest(http.MethodGet, "/api/oauth/auth-flow-test?state="+providerErrorToken+"&error=access_denied", nil)
	response = httptest.NewRecorder()
	router.ServeHTTP(response, request)
	_, err = model.GetAuthFlow(providerErrorToken, model.AuthFlowMatch{Purpose: model.AuthFlowPurposeOAuth})
	assert.ErrorIs(t, err, model.ErrAuthFlowConsumed)
	assert.Equal(t, 1, provider.exchangeCalls)
	assert.Equal(t, 1, provider.userInfoCalls)
}

func TestOAuthBindProviderErrorConsumesSessionBoundFlow(t *testing.T) {
	provider := setupAuthFlowControllerTest(t)
	flowToken, _, err := model.CreateAuthFlow(model.AuthFlowCreate{
		Purpose: model.AuthFlowPurposeOAuth, Provider: "auth-flow-test", Intent: model.AuthFlowIntentBind,
		UserId: 42, SessionId: "session-42", Payload: `{}`, ExpiresAt: time.Now().Add(time.Minute),
	})
	require.NoError(t, err)
	router := gin.New()
	router.Use(func(c *gin.Context) {
		c.Set("id", 42)
		c.Set("session_id", "session-42")
		c.Set("auth_version", int64(1))
		c.Set("session_version", int64(1))
		c.Next()
	})
	router.GET("/api/oauth/:provider", HandleOAuth)
	request := httptest.NewRequest(http.MethodGet, "/api/oauth/auth-flow-test?state="+flowToken+"&error=access_denied&error_description=cancelled", nil)
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)

	assert.Equal(t, http.StatusOK, response.Code)
	_, err = model.GetAuthFlow(flowToken, model.AuthFlowMatch{Purpose: model.AuthFlowPurposeOAuth})
	assert.ErrorIs(t, err, model.ErrAuthFlowConsumed)
	assert.Zero(t, provider.exchangeCalls)
	assert.Zero(t, provider.userInfoCalls)
}

// legacyGitHubTestProvider stores a legacy (login-name) GitHub binding and
// reports the verified emails GitHub would return for the signed-in account.
type legacyGitHubTestProvider struct {
	authFlowTestOAuthProvider
	legacyID       string
	user           model.User
	verifiedEmails []string
	emailsErr      error
}

func (p *legacyGitHubTestProvider) IsUserIDTaken(id string) bool { return id == p.legacyID }
func (p *legacyGitHubTestProvider) FillUserByProviderID(user *model.User, id string) error {
	if id == p.legacyID {
		*user = p.user
	}
	return nil
}
func (p *legacyGitHubTestProvider) GetVerifiedEmails(context.Context, *oauth.OAuthToken) ([]string, error) {
	return p.verifiedEmails, p.emailsErr
}

// A GitHub login name can be renamed and re-registered by someone else, so a
// legacy login-name match alone must never sign into the existing account.
func TestLegacyGitHubBindingMigratesOnlyWithVerifiedEmail(t *testing.T) {
	setupAuthFlowControllerTest(t)
	require.NoError(t, model.DB.AutoMigrate(&model.User{}))
	previousRegister := common.RegisterEnabled
	common.RegisterEnabled = false
	t.Cleanup(func() { common.RegisterEnabled = previousRegister })

	for _, tc := range []struct {
		name      string
		legacyID  string
		emails    []string
		emailsErr error
		migrated  bool
	}{
		{name: "verified email matches", legacyID: "alice", emails: []string{"other@example.com", "Alice@Example.com"}, migrated: true},
		{name: "no matching verified email", legacyID: "alice", emails: []string{"attacker@example.com"}},
		{name: "verified emails unavailable", legacyID: "alice", emailsErr: errors.New("github down")},
		{name: "all-digit legacy id never compared", legacyID: "12345", emails: []string{"alice@example.com"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			existing := model.User{Username: "legacy-" + strings.ReplaceAll(tc.name, " ", "-"), AffCode: strings.ReplaceAll(tc.name, " ", "-"), Email: "alice@example.com", GitHubId: tc.legacyID, Status: common.UserStatusEnabled}
			require.NoError(t, model.DB.Create(&existing).Error)
			provider := &legacyGitHubTestProvider{legacyID: tc.legacyID, user: existing, verifiedEmails: tc.emails, emailsErr: tc.emailsErr}
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			c.Request = httptest.NewRequest(http.MethodGet, "/api/oauth/github", nil)

			user, err := findOrCreateOAuthUser(c, provider, &oauth.OAuthUser{ProviderUserID: "987654", Extra: map[string]any{"legacy_id": tc.legacyID}}, &oauth.OAuthToken{}, "")

			var stored model.User
			require.NoError(t, model.DB.First(&stored, existing.Id).Error)
			if tc.migrated {
				require.NoError(t, err)
				assert.Equal(t, existing.Id, user.Id)
				assert.Equal(t, "987654", stored.GitHubId)
				return
			}
			assert.Nil(t, user)
			assert.Error(t, err, "a legacy match without evidence must not sign in")
			assert.Equal(t, tc.legacyID, stored.GitHubId)
		})
	}
}
