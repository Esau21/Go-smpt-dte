package mail_gmail

import (
	"time"

	"github.com/jellydator/ttlcache/v3"
	"golang.org/x/oauth2"
	"google.golang.org/api/gmail/v1"
)

const (
	callbackPath = "/api/auth/google/callback"
	flowTTL      = 10 * time.Minute
	sessionTTL   = 8 * time.Hour
)

type loginFlow struct {
	verifier string
	nonce    string
}

type session struct {
	sub   string
	email string
	gmail *gmail.Service
}

type GmailClient struct {
	oauth      oauth2.Config
	flows      *ttlcache.Cache[string, loginFlow]
	sessions   *ttlcache.Cache[string, session]
	afterLogin string
	secure     bool
}
