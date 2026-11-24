package mail_gmail

import (
	"encoding/base64"
	"net/http"
	"strings"

	"github.com/Esau21/Go-smpt-dte/internal/validations"
	"github.com/gin-gonic/gin"
	"github.com/jhillyerd/enmime/v2"
)

func (*GmailClient) GetMessage(c *gin.Context) {
	value, _ := c.Get(sessionContextKey)
	s, ok := value.(session)
	if !ok {
		c.Abort()
		validations.Fail(c, http.StatusUnauthorized, "login_required")
		return
	}

	getMessage(c, s)
}

func getMessage(c *gin.Context, s session) {
	m, err := s.gmail.Users.Messages.Get("me", c.Param("id")).
		Format("raw").
		Fields("id,threadId,raw").
		Context(c.Request.Context()).Do()
	if err != nil {
		gmailFailure(c, err)
		return
	}

	raw := strings.NewReader(strings.TrimRight(m.Raw, "="))
	mail, err := enmime.ReadEnvelope(base64.NewDecoder(base64.RawURLEncoding, raw))
	if err != nil {
		validations.Fail(c, 422, "unreadable_message")
		return
	}

	result := fields(m, mail.GetHeader)
	result["to"] = mail.GetHeader("To")
	result["text"] = mail.Text
	c.JSON(200, result)
}
