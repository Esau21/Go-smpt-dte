package mail_gmail

import (
	"net/http"

	"github.com/Esau21/Go-smpt-dte/internal/validations"
	"github.com/gin-gonic/gin"
)

const sessionContextKey = "internal/mail/gmail.session"

func (gc *GmailClient) WithSession(next gin.HandlerFunc) gin.HandlerFunc {
	return gc.withSession(func(c *gin.Context, s session) {
		c.Set(sessionContextKey, s)
		next(c)
	})
}

func (gc *GmailClient) withSession(next func(*gin.Context, session)) gin.HandlerFunc {
	return func(c *gin.Context) {
		id, _ := c.Cookie("gmail_session")
		entry := gc.sessions.Get(id)
		if entry == nil {
			validations.Fail(c, 401, "login_required")
			return
		}

		next(c, entry.Value())
		if c.Writer.Status() == http.StatusUnauthorized {
			gc.sessions.Delete(id)
		}
	}
}
