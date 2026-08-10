package service

import (
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

const RefreshCookieName = "new_api_refresh"

var (
	ErrLoginSessionInvalid  = errors.New("login session is invalid")
	ErrLoginSessionRevoked  = errors.New("login session is revoked")
	ErrLoginSessionMismatch = errors.New("login session does not match the expected session")
	ErrRefreshTokenInvalid  = errors.New("refresh token is invalid")
	ErrRefreshRace          = errors.New("refresh token was already rotated")
)

type LoginSessionView struct {
	SID          string `json:"sid"`
	Current      bool   `json:"current"`
	LoginMethod  string `json:"login_method"`
	IP           string `json:"ip"`
	UserAgent    string `json:"user_agent"`
	CreatedAt    int64  `json:"created_at"`
	LastActiveAt int64  `json:"last_active_at"`
	ExpiresAt    int64  `json:"expires_at"`
}

type AuthBundle struct {
	AccessToken     string           `json:"access_token"`
	TokenType       string           `json:"token_type"`
	AccessExpiresAt int64            `json:"access_expires_at"`
	Session         LoginSessionView `json:"session"`
	RefreshToken    string           `json:"-"`
}

func CreateLoginSession(userID int, loginMethod, ip, userAgent string) (*AuthBundle, error) {
	return createLoginSession(userID, 0, loginMethod, ip, userAgent)
}

func CreateLoginSessionAtAuthVersion(userID int, expectedAuthVersion int64, loginMethod, ip, userAgent string) (*AuthBundle, error) {
	if expectedAuthVersion <= 0 {
		return nil, ErrLoginSessionInvalid
	}
	return createLoginSession(userID, expectedAuthVersion, loginMethod, ip, userAgent)
}

func createLoginSession(userID int, expectedAuthVersion int64, loginMethod, ip, userAgent string) (*AuthBundle, error) {
	user, err := model.GetUserCache(userID)
	if err != nil {
		return nil, err
	}
	// 受限账号可以建会话(它要能登录进来提工单),但状态必须是已知的可持有
	// 会话状态 —— status=0 这类半截数据一律拒。
	if !common.UserStatusAllowsSession(user.Status) || user.AuthVersion <= 0 {
		return nil, ErrLoginSessionInvalid
	}
	if expectedAuthVersion > 0 && user.AuthVersion != expectedAuthVersion {
		return nil, ErrLoginSessionRevoked
	}
	now := time.Now().Unix()
	activeCount, err := model.CountActiveUserSessions(userID, now)
	if err != nil {
		return nil, err
	}
	if activeCount >= int64(common.UserSessionActiveLimit) {
		return nil, model.ErrUserSessionLimit
	}
	issuanceCount, err := model.CountUserSessionsCreatedSince(userID, now-common.UserSessionIssuanceWindowSeconds)
	if err != nil {
		return nil, err
	}
	if issuanceCount >= int64(common.UserSessionIssuanceLimit) {
		return nil, model.ErrUserSessionIssuanceLimit
	}
	refreshSecret, err := common.GenerateRandomCharsKey(64)
	if err != nil {
		return nil, err
	}
	session := &model.UserSession{
		SID:             uuid.NewString(),
		UserID:          userID,
		Version:         1,
		UserAuthVersion: user.AuthVersion,
		Status:          model.UserSessionStatusActive,
		RefreshHash:     hashRefreshSecret(refreshSecret),
		LoginMethod:     strings.TrimSpace(loginMethod),
		IP:              truncateAuthMetadata(ip, 64),
		UserAgent:       truncateAuthMetadata(userAgent, 512),
		CreatedAt:       now,
		LastActiveAt:    now,
		ExpiresAt:       time.Unix(now, 0).Add(LoginSessionTTL).Unix(),
	}
	if session.LoginMethod == "" {
		session.LoginMethod = "unknown"
	}
	if err := model.CreateUserSession(session); err != nil {
		return nil, err
	}
	bundle, err := issueAuthBundle(session, session.SID+"."+refreshSecret, true)
	if err != nil {
		_, _ = model.RevokeUserSession(userID, session.SID, "token_issue_failed")
		return nil, err
	}
	return bundle, nil
}

func ValidateLoginSession(identity AuthIdentity) (*model.UserSession, *model.UserBase, error) {
	session, err := model.GetUserSessionCached(identity.SessionID)
	if err != nil {
		if errors.Is(err, model.ErrUserSessionInactive) {
			return nil, nil, ErrLoginSessionRevoked
		}
		return nil, nil, err
	}
	now := time.Now().Unix()
	if session.UserID != identity.UserID || session.Status != model.UserSessionStatusActive || session.RevokedAt != 0 || session.ExpiresAt <= now || session.Version != identity.SessionVersion || session.UserAuthVersion != identity.UserAuthVersion {
		return nil, nil, ErrLoginSessionRevoked
	}
	user, err := model.GetUserCache(identity.UserID)
	if err != nil {
		return nil, nil, err
	}
	// 这里放宽到 UserStatusAllowsSession 是白名单能生效的前提:
	// 本函数是 authHelper 的上游,若在这里就把受限账号打成「会话已撤销」,
	// middleware 的白名单永远没有机会被评估,请求也会以 401 返回而被前端
	// 当成掉线。真正的资源判据在 middleware/restricted_user.go。
	if !common.UserStatusAllowsSession(user.Status) || user.AuthVersion != identity.UserAuthVersion {
		return nil, nil, ErrLoginSessionRevoked
	}
	return session, user, nil
}

// ValidateSessionReference validates a server-side flow bound to an existing
// dashboard session without requiring an access token on the callback request.
func ValidateSessionReference(userID int, sid string) (AuthIdentity, error) {
	if userID <= 0 || strings.TrimSpace(sid) == "" {
		return AuthIdentity{}, ErrLoginSessionInvalid
	}
	session, err := model.GetUserSessionCached(sid)
	if err != nil {
		return AuthIdentity{}, err
	}
	identity := AuthIdentity{
		UserID:          userID,
		SessionID:       sid,
		UserAuthVersion: session.UserAuthVersion,
		SessionVersion:  session.Version,
	}
	if _, _, err := ValidateLoginSession(identity); err != nil {
		return AuthIdentity{}, err
	}
	return identity, nil
}

// AdvanceCurrentSessionSecurity increments the user's global auth version,
// preserves only the current browser session at a new session version and
// returns a replacement access token. Call after a successful 2FA/passkey
// security-setting mutation that did not already advance AuthVersion.
func AdvanceCurrentSessionSecurity(identity AuthIdentity, reason string) (*AuthBundle, error) {
	nextUserAuthVersion, err := model.BumpUserAuthVersion(identity.UserID)
	if err != nil {
		return nil, err
	}
	return advanceCurrentSessionToVersion(identity, nextUserAuthVersion, reason)
}

// AdvanceCurrentSessionToUserVersion is used when the security mutation and
// AuthVersion increment were committed in the same transaction (for example,
// a password change).
func AdvanceCurrentSessionToUserVersion(identity AuthIdentity, reason string) (*AuthBundle, error) {
	user, err := model.GetUserCache(identity.UserID)
	if err != nil {
		return nil, err
	}
	// 刻意**不**放宽到 UserStatusAllowsSession:本函数只在改密码/改 2FA/改 passkey
	// 这类凭据面变更之后调用,而那几条路由都不在受限账号的白名单里。
	// 保持严格判据是第二道自锁 —— 万一将来有人往白名单里加错一条,
	// 受限账号也换不掉自己的凭据。
	if user.Status != common.UserStatusEnabled || user.AuthVersion <= identity.UserAuthVersion {
		return nil, ErrLoginSessionRevoked
	}
	return advanceCurrentSessionToVersion(identity, user.AuthVersion, reason)
}

func advanceCurrentSessionToVersion(identity AuthIdentity, nextUserAuthVersion int64, reason string) (*AuthBundle, error) {
	session, err := model.AdvanceUserSessionAuthVersion(
		identity.UserID,
		identity.SessionID,
		identity.SessionVersion,
		identity.UserAuthVersion,
		nextUserAuthVersion,
	)
	if err != nil {
		return nil, err
	}
	if _, err := model.RevokeOtherUserSessions(identity.UserID, identity.SessionID, reason); err != nil {
		return nil, err
	}
	return issueAuthBundle(session, "", true)
}

func RefreshLoginSession(rawRefreshToken, expectedSID, ip, userAgent string) (*AuthBundle, *model.User, error) {
	sid, secret, ok := splitRefreshToken(rawRefreshToken)
	if !ok {
		return nil, nil, ErrRefreshTokenInvalid
	}
	if expectedSID = strings.TrimSpace(expectedSID); expectedSID != "" && expectedSID != sid {
		return nil, nil, ErrLoginSessionMismatch
	}
	session, err := model.GetUserSessionCached(sid)
	if err != nil {
		if errors.Is(err, model.ErrUserSessionInactive) {
			return nil, nil, ErrLoginSessionRevoked
		}
		return nil, nil, ErrRefreshTokenInvalid
	}
	if session.Status != model.UserSessionStatusActive || session.RevokedAt != 0 || session.ExpiresAt <= time.Now().Unix() {
		return nil, nil, ErrLoginSessionRevoked
	}
	userCache, err := model.GetUserCache(session.UserID)
	if err != nil {
		return nil, nil, err
	}
	currentUser, err := model.GetUserById(session.UserID, false)
	if err != nil {
		return nil, nil, err
	}
	// access_token 只有 900 秒。若这里继续要求 Enabled,受限账号即使能登录也会在
	// 15 分钟后被强制登出**并且会话被物理撤销**(下面那次 RevokeUserSession),
	// 于是「被禁用仍可提工单」在体感上是一个 15 分钟的窗口。
	// auth_version 不匹配仍然撤销:那是密码/角色/分组变更,与本改动无关。
	if !common.UserStatusAllowsSession(userCache.Status) || userCache.AuthVersion != session.UserAuthVersion ||
		!common.UserStatusAllowsSession(currentUser.Status) || currentUser.AuthVersion != session.UserAuthVersion {
		_, _ = model.RevokeUserSession(session.UserID, session.SID, "user_security_changed")
		return nil, nil, ErrLoginSessionRevoked
	}
	nextSecret := deriveNextRefreshSecret(sid, secret)
	rotated, err := model.RotateUserSessionRefresh(session.UserID, sid, hashRefreshSecret(secret), hashRefreshSecret(nextSecret), time.Now().Unix(), RefreshReplayWindow)
	if err != nil {
		if errors.Is(err, model.ErrUserSessionRefreshRace) && rotated != nil &&
			hashRefreshSecret(nextSecret) == rotated.RefreshHash {
			bundle, issueErr := issueAuthBundle(rotated, sid+"."+nextSecret, true)
			if issueErr != nil {
				return nil, nil, issueErr
			}
			return bundle, currentUser, nil
		}
		if errors.Is(err, model.ErrUserSessionRefreshReuse) {
			return nil, nil, ErrLoginSessionRevoked
		}
		if errors.Is(err, model.ErrUserSessionRefreshInvalid) {
			return nil, nil, ErrRefreshTokenInvalid
		}
		if errors.Is(err, model.ErrUserSessionRefreshRace) {
			return nil, nil, ErrRefreshRace
		}
		return nil, nil, err
	}
	rotated.IP = truncateAuthMetadata(ip, 64)
	rotated.UserAgent = truncateAuthMetadata(userAgent, 512)
	bundle, err := issueAuthBundle(rotated, sid+"."+nextSecret, true)
	if err != nil {
		return nil, nil, err
	}
	return bundle, currentUser, nil
}

func RevokeByRefreshToken(rawRefreshToken, expectedSID, reason string) error {
	sid, secret, ok := splitRefreshToken(rawRefreshToken)
	if !ok {
		return nil
	}
	if expectedSID = strings.TrimSpace(expectedSID); expectedSID != "" && expectedSID != sid {
		return ErrLoginSessionMismatch
	}
	_, err := model.RevokeUserSessionByRefreshHash(sid, hashRefreshSecret(secret), reason)
	return err
}

func RefreshTokenSID(rawRefreshToken string) (string, bool) {
	sid, _, ok := splitRefreshToken(rawRefreshToken)
	return sid, ok
}

func ListLoginSessions(userID int, currentSID string) ([]LoginSessionView, error) {
	sessions, err := model.ListActiveUserSessions(userID, currentSID, time.Now().Unix())
	if err != nil {
		return nil, err
	}
	views := make([]LoginSessionView, 0, len(sessions))
	for i := range sessions {
		views = append(views, sessionView(&sessions[i], sessions[i].SID == currentSID))
	}
	return views, nil
}

func WriteRefreshCookie(c *gin.Context, rawToken string) {
	expiresAt := time.Now().Add(LoginSessionTTL)
	if sid, _, ok := splitRefreshToken(rawToken); ok {
		if session, err := model.GetUserSessionCached(sid); err == nil && session.ExpiresAt > time.Now().Unix() {
			expiresAt = time.Unix(session.ExpiresAt, 0)
		}
	}
	maxAge := int(time.Until(expiresAt) / time.Second)
	if maxAge < 1 {
		maxAge = 1
	}
	http.SetCookie(c.Writer, &http.Cookie{
		Name:     RefreshCookieName,
		Value:    rawToken,
		Path:     "/api/user/auth",
		MaxAge:   maxAge,
		Expires:  expiresAt,
		HttpOnly: true,
		Secure:   common.SessionCookieSecure,
		SameSite: http.SameSiteStrictMode,
	})
}

func ClearRefreshCookie(c *gin.Context) {
	http.SetCookie(c.Writer, &http.Cookie{
		Name:     RefreshCookieName,
		Value:    "",
		Path:     "/api/user/auth",
		MaxAge:   -1,
		Expires:  time.Unix(1, 0),
		HttpOnly: true,
		Secure:   common.SessionCookieSecure,
		SameSite: http.SameSiteStrictMode,
	})
}

func issueAuthBundle(session *model.UserSession, rawRefreshToken string, current bool) (*AuthBundle, error) {
	identity := AuthIdentity{
		UserID:          session.UserID,
		SessionID:       session.SID,
		UserAuthVersion: session.UserAuthVersion,
		SessionVersion:  session.Version,
	}
	accessToken, accessExpiresAt, err := IssueAccessToken(identity)
	if err != nil {
		return nil, err
	}
	return &AuthBundle{
		AccessToken:     accessToken,
		TokenType:       "Bearer",
		AccessExpiresAt: accessExpiresAt,
		Session:         sessionView(session, current),
		RefreshToken:    rawRefreshToken,
	}, nil
}

func sessionView(session *model.UserSession, current bool) LoginSessionView {
	return LoginSessionView{
		SID:          session.SID,
		Current:      current,
		LoginMethod:  session.LoginMethod,
		IP:           session.IP,
		UserAgent:    session.UserAgent,
		CreatedAt:    session.CreatedAt,
		LastActiveAt: session.LastActiveAt,
		ExpiresAt:    session.ExpiresAt,
	}
}

func splitRefreshToken(raw string) (string, string, bool) {
	sid, secret, ok := strings.Cut(strings.TrimSpace(raw), ".")
	if !ok || sid == "" || secret == "" || strings.Contains(secret, ".") {
		return "", "", false
	}
	if _, err := uuid.Parse(sid); err != nil {
		return "", "", false
	}
	return sid, secret, true
}

func hashRefreshSecret(secret string) string {
	return common.GenerateHMACWithKey(authSigningKey("refresh"), secret)
}

func deriveNextRefreshSecret(sid, currentSecret string) string {
	return common.GenerateHMACWithKey(authSigningKey("refresh-rotate"), sid+"."+currentSecret)
}

func truncateAuthMetadata(value string, max int) string {
	value = strings.TrimSpace(value)
	if len(value) <= max {
		return value
	}
	return value[:max]
}

func authSessionErrorCode(err error) (int, string) {
	switch {
	case errors.Is(err, model.ErrUserSessionLimit):
		return http.StatusConflict, "AUTH_SESSION_LIMIT"
	case errors.Is(err, model.ErrUserSessionIssuanceLimit):
		return http.StatusTooManyRequests, "AUTH_SESSION_ISSUANCE_LIMIT"
	case errors.Is(err, ErrLoginSessionMismatch):
		return http.StatusConflict, "AUTH_SESSION_MISMATCH"
	case errors.Is(err, ErrRefreshRace):
		return http.StatusConflict, "AUTH_REFRESH_RACE"
	case errors.Is(err, ErrAuthTokenExpired):
		return http.StatusUnauthorized, "AUTH_TOKEN_EXPIRED"
	case errors.Is(err, ErrLoginSessionRevoked):
		return http.StatusUnauthorized, "AUTH_SESSION_REVOKED"
	case errors.Is(err, ErrRefreshTokenInvalid), errors.Is(err, ErrAuthTokenInvalid):
		return http.StatusUnauthorized, "AUTH_UNAUTHORIZED"
	default:
		return http.StatusInternalServerError, "AUTH_INTERNAL_ERROR"
	}
}

func AuthSessionErrorCode(err error) (int, string) {
	return authSessionErrorCode(err)
}

func FormatAuthError(err error) string {
	if err == nil {
		return ""
	}
	return fmt.Sprintf("authentication failed: %v", err)
}
