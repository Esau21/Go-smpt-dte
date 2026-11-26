package mail

import (
	"errors"
	"strings"

	mail_gmail "github.com/Esau21/Go-smpt-dte/internal/mail/gmail"
	mail_outlook "github.com/Esau21/Go-smpt-dte/internal/mail/outlook"
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

	// Providers lista los proveedores con los que se puede iniciar sesión
	// ("gmail", "outlook"), en el orden en que se ofrecen.
	Providers() []string
}

var errNoProvider = errors.New("no mail provider for this request")

func NewMail(origin, afterlogin string) (IMail, error) {
	mail, err := validations.RequiredEnv("MAIL_PROVIDER")

	if err != nil {
		return nil, err
	}

	// MAIL_PROVIDER admite uno o varios, separados por coma: "GMAIL,OUTLOOK".
	all := &multi{providers: map[string]provider{}}
	for _, name := range strings.Split(mail, ",") {
		var (
			p   provider
			err error
		)
		name = strings.ToLower(strings.TrimSpace(name))
		switch name {
		case "gmail":
			p, err = mail_gmail.SetupGmail(origin, afterlogin)
		case "outlook":
			p, err = mail_outlook.Setup(origin, afterlogin)
		case "":
			continue
		default:
			return nil, errors.New("Mail provider not supported: " + name)
		}
		if err != nil {
			return nil, err
		}
		if _, repeated := all.providers[name]; !repeated {
			all.names = append(all.names, name)
			all.providers[name] = p
		}
	}

	switch len(all.names) {
	case 0:
		return nil, errors.New("Mail provider not supported")
	case 1:
		// Con un solo proveedor no hace falta repartir nada.
		return all.providers[all.names[0]], nil
	}
	return all, nil
}
