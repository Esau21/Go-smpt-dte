package mail

import (
	"errors"

	mail_gmail "github.com/Esau21/Go-smpt-dte/internal/mail/gmail"
	"github.com/Esau21/Go-smpt-dte/internal/validations"
	"github.com/gin-gonic/gin"
)

type IMail interface {
	Login(c *gin.Context)
	Callback(c *gin.Context)
	Logout(c *gin.Context)
	WithSession(next gin.HandlerFunc) gin.HandlerFunc
	Me(c *gin.Context)
	ListMessages(c *gin.Context)
	GetMessage(c *gin.Context)
	GetDTE(c *gin.Context)
	GetAttachment(c *gin.Context)
}

func NewMail(origin, afterlogin string) (IMail, error) {
	mail, err := validations.RequiredEnv("MAIL_PROVIDER")

	if err != nil {
		return nil, err
	}

	switch mail {
	case "GMAIL":
		return mail_gmail.SetupGmail(origin, afterlogin)
	default:
		return nil, errors.New("Mail provider not supported")
	}
}
