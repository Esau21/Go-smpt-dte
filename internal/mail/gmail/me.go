package mail_gmail

import (
	"net/http"

	"github.com/Esau21/Go-smpt-dte/internal/validations"
	"github.com/gin-gonic/gin"
)

func (*GmailClient) Me(c *gin.Context) {
	value, _ := c.Get(sessionContextKey)
	s, ok := value.(session)
	if !ok {
		c.Abort()
		validations.Fail(c, http.StatusUnauthorized, "login_required")
		return
	}

	me(c, s)
}

func me(c *gin.Context, s session) {
	c.JSON(200, gin.H{"id": s.sub, "email": s.email, "provider": "gmail"})
}
