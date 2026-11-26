package mail_gmail

import (
	"encoding/base64"
	"errors"
	"strings"

	"github.com/Esau21/Go-smpt-dte/internal/mailbox"
	"github.com/gin-gonic/gin"
	"github.com/jhillyerd/enmime/v2"
)

// dteQuery encuentra los correos que traen un .json adjunto.
const dteQuery = "filename:json"

var errNoSession = errors.New("no mail session")

func sessionOf(c *gin.Context) (session, bool) {
	value, _ := c.Get(sessionContextKey)
	s, ok := value.(session)
	return s, ok
}

// Account devuelve la cuenta con sesión activa. Requiere WithSession.
func (*GmailClient) Account(c *gin.Context) (mailbox.Account, bool) {
	s, ok := sessionOf(c)
	if !ok {
		return mailbox.Account{}, false
	}
	return mailbox.Account{Provider: "gmail", ID: s.sub, Email: s.email}, true
}

// ListDTEMessages devuelve una página de identificadores de correos con un
// .json adjunto, del más reciente al más antiguo.
func (*GmailClient) ListDTEMessages(c *gin.Context, pageToken string, limit int) ([]string, string, error) {
	s, ok := sessionOf(c)
	if !ok {
		return nil, "", errNoSession
	}

	page, err := s.gmail.Users.Messages.List("me").
		Q(dteQuery).
		MaxResults(int64(limit)).
		PageToken(pageToken).
		Context(c.Request.Context()).Do()
	if err != nil {
		return nil, "", err
	}

	ids := make([]string, len(page.Messages))
	for i, m := range page.Messages {
		ids[i] = m.Id
	}
	return ids, page.NextPageToken, nil
}

// FetchDTE descarga un correo y devuelve su .json adjunto. Si el correo no
// trae ninguno, devuelve nil sin error.
func (*GmailClient) FetchDTE(c *gin.Context, id string) (*mailbox.Message, error) {
	s, ok := sessionOf(c)
	if !ok {
		return nil, errNoSession
	}

	m, err := s.gmail.Users.Messages.Get("me", id).
		Format("raw").Fields("id,raw").
		Context(c.Request.Context()).Do()
	if err != nil {
		return nil, err
	}

	raw := strings.NewReader(strings.TrimRight(m.Raw, "="))
	mail, err := enmime.ReadEnvelope(base64.NewDecoder(base64.RawURLEncoding, raw))
	if err != nil {
		return nil, nil
	}
	content, ok := findDTE(mail)
	if !ok {
		return nil, nil
	}

	return &mailbox.Message{
		ID:      m.Id,
		From:    mail.GetHeader("From"),
		Subject: mail.GetHeader("Subject"),
		DTE:     content,
	}, nil
}

// ErrorStatus traduce un error del proveedor al código HTTP que le
// corresponde (401 si la sesión ya no sirve, 429 si hay límite, etc.).
func (*GmailClient) ErrorStatus(err error) int {
	if err == errNoSession {
		return 401
	}
	return googleStatus(err)
}
