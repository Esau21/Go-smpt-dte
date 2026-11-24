package mail_gmail

import (
	"net/http"

	"github.com/gin-gonic/gin"
)

func (gc *GmailClient) Logout(c *gin.Context) {
	id, _ := c.Cookie("gmail_session")
	gc.sessions.Delete(id)

	state, _ := c.Cookie("gmail_flow")
	gc.flows.Delete(state)

	gc.cookie(c, "gmail_flow", "", -1)
	gc.cookie(c, "gmail_session", "", -1)
	c.Status(http.StatusNoContent)
}
