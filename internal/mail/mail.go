package mail

import (
	"errors"

	mail_gmail "github.com/Esau21/Go-smpt-dte/internal/mail/gmail"
	"github.com/Esau21/Go-smpt-dte/internal/mailbox"
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

	// Lo que usa el módulo de facturas para leer los DTE del buzón.
	Account(c *gin.Context) (mailbox.Account, bool)
	ListDTEMessages(c *gin.Context, pageToken string, limit int) (ids []string, next string, err error)
	FetchDTE(c *gin.Context, id string) (*mailbox.Message, error)
	FetchAttachment(c *gin.Context, id string, index int) (*mailbox.Attachment, error)
	ErrorStatus(err error) int
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
