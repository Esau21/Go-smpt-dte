package mail_gmail

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"net/http"
	"slices"
	"strings"
	"time"

	"cloud.google.com/go/auth/credentials/idtoken"
	"github.com/Esau21/Go-smpt-dte/internal/validations"
	"github.com/gin-gonic/gin"
	"golang.org/x/oauth2"
	"google.golang.org/api/gmail/v1"
	"google.golang.org/api/option"
)

func (gc *GmailClient) Callback(c *gin.Context) {
	state, err := c.Cookie("gmail_flow")
	if err != nil || state == "" ||
		subtle.ConstantTimeCompare([]byte(state), []byte(c.Query("state"))) != 1 {
		validations.Fail(c, 400, "invalid_oauth_state")
		return
	}

	entry, ok := gc.flows.GetAndDelete(state)
	gc.cookie(c, "gmail_flow", "", -1)
	if !ok {
		validations.Fail(c, 400, "expired_or_used_login")
		return
	}
	if c.Query("error") != "" || c.Query("code") == "" {
		validations.Fail(c, 403, "authorization_not_completed")
		return
	}

	ctx, cancel := context.WithTimeout(c.Request.Context(), 15*time.Second)
	defer cancel()

	flow := entry.Value()
	token, err := gc.oauth.Exchange(ctx, c.Query("code"), oauth2.VerifierOption(flow.verifier))
	if err != nil {
		validations.Fail(c, googleStatus(err), "code_exchange_failed")
		return
	}

	rawID, _ := token.Extra("id_token").(string)
	user, err := idtoken.Validate(ctx, rawID, gc.oauth.ClientID)
	if err != nil ||
		(user.Issuer != "https://accounts.google.com" && user.Issuer != "accounts.google.com") ||
		user.Subject == "" || user.Claims["nonce"] != flow.nonce {
		validations.Fail(c, 401, "invalid_google_identity")
		return
	}

	email, _ := user.Claims["email"].(string)
	verified := user.Claims["email_verified"]
	if email == "" || (verified != true && verified != "true") {
		validations.Fail(c, 403, "verified_email_required")
		return
	}

	if scopes, ok := token.Extra("scope").(string); ok &&
		!slices.Contains(strings.Fields(scopes), gmail.GmailReadonlyScope) {
		validations.Fail(c, 403, "gmail_permission_required")
		return
	}

	clientCtx := context.WithValue(context.Background(), oauth2.HTTPClient,
		&http.Client{Timeout: 15 * time.Second})
	service, err := gmail.NewService(clientCtx, option.WithHTTPClient(gc.oauth.Client(clientCtx, token)))
	if err != nil {
		validations.Fail(c, 500, "gmail_client_failed")
		return
	}

	ttl := sessionTTL
	if token.RefreshToken == "" && !token.Expiry.IsZero() {
		ttl = min(ttl, time.Until(token.Expiry.Add(-10*time.Second)))
	}
	if ttl < time.Second {
		validations.Fail(c, 401, "expired_google_token")
		return
	}

	oldID, _ := c.Cookie("gmail_session")
	gc.sessions.Delete(oldID)

	id := rand.Text()
	gc.sessions.Set(id, session{sub: user.Subject, email: email, gmail: service}, ttl)
	gc.cookie(c, "gmail_session", id, int(ttl.Seconds()))
	c.Redirect(http.StatusSeeOther, gc.afterLogin)
}
