package mail_gmail

import (
	"crypto/rand"
	"net/http"

	"github.com/gin-gonic/gin"
	"golang.org/x/oauth2"
)

func (gc *GmailClient) Login(c *gin.Context) {
	state := rand.Text()
	flow := loginFlow{
		verifier: oauth2.GenerateVerifier(),
		nonce:    rand.Text(),
	}

	gc.flows.Set(state, flow, flowTTL)
	gc.cookie(c, "gmail_flow", state, int(flowTTL.Seconds()))

	url := gc.oauth.AuthCodeURL(state,
		oauth2.S256ChallengeOption(flow.verifier),
		oauth2.SetAuthURLParam("nonce", flow.nonce),
		oauth2.AccessTypeOffline,
		oauth2.SetAuthURLParam("prompt", "consent"),
	)
	c.Redirect(http.StatusFound, url)
}
