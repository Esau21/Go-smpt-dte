package mail_gmail

import (
	"net/http"

	"github.com/gin-gonic/gin"
)

func (gc *GmailClient) cookie(c *gin.Context, name, value string, seconds int) {
	c.SetSameSite(http.SameSiteLaxMode)
	c.SetCookie(name, value, seconds, "/api", "", gc.secure, true)
}
