package mail_gmail

import (
	"encoding/base64"
	"strings"

	"github.com/Esau21/Go-smpt-dte/internal/mailbox"
	"github.com/gin-gonic/gin"
	"github.com/jhillyerd/enmime/v2"
)

// FetchAttachment descarga un adjunto de un correo, con los datos del
// correo del que salió. Si el correo no tiene ese adjunto devuelve nil.
func (*GmailClient) FetchAttachment(c *gin.Context, id string, index int) (*mailbox.Attachment, error) {
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

	parts := files(mail)
	if index < 0 || index >= len(parts) {
		return nil, nil
	}
	return &mailbox.Attachment{
		MessageID: m.Id,
		Filename:  parts[index].FileName,
		Content:   parts[index].Content,
		From:      mail.GetHeader("From"),
		Subject:   mail.GetHeader("Subject"),
	}, nil
}
