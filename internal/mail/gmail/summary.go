package mail_gmail

import (
	"html"
	"slices"
	"mime"
	"net/textproto"

	"github.com/gin-gonic/gin"
	"golang.org/x/net/html/charset"
	"google.golang.org/api/gmail/v1"
)

func summary(m *gmail.Message) gin.H {
	headers := make(textproto.MIMEHeader)
	decoder := mime.WordDecoder{CharsetReader: charset.NewReaderLabel}

	if m.Payload != nil {
		for _, h := range m.Payload.Headers {
			value, err := decoder.DecodeHeader(h.Value)
			if err != nil {
				value = h.Value
			}
			headers.Add(h.Name, value)
		}
	}
	result := fields(m, headers.Get)
	// Las primeras palabras del correo y si aún no se ha leído, para que la
	// lista se parezca a la bandeja de Gmail.
	result["snippet"] = html.UnescapeString(m.Snippet)
	result["unread"] = slices.Contains(m.LabelIds, "UNREAD")
	return result
}
