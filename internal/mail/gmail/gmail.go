package mail_gmail

import (
	"strings"

	"github.com/Esau21/Go-smpt-dte/internal/validations"
	"golang.org/x/oauth2"
	"google.golang.org/api/gmail/v1"
)

var SetupGmail = func(origin, afterLogin string) (*GmailClient, error) {
	id, err := validations.RequiredEnv("OAUTH_CLIENT_ID")

	if err != nil {
		return nil, err
	}

	secret, err := validations.RequiredEnv("OAUTH_CLIENT_SECRET")

	if err != nil {
		return nil, err
	}

	return &GmailClient{
		oauth: oauth2.Config{
			ClientID:     id,
			ClientSecret: secret,
			Endpoint: oauth2.Endpoint{
				AuthURL:   "https://accounts.google.com/o/oauth2/v2/auth",
				TokenURL:  "https://oauth2.googleapis.com/token",
				AuthStyle: oauth2.AuthStyleInParams,
			},
			RedirectURL: origin + callbackPath,
			Scopes:      []string{"openid", "email", gmail.GmailReadonlyScope},
		},
		flows:      newCache[loginFlow](flowTTL),
		sessions:   newCache[session](sessionTTL),
		afterLogin: afterLogin,
		secure:     strings.HasPrefix(origin, "https://"),
	}, nil
}
