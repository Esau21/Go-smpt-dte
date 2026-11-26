package mail_gmail

import (
	"encoding/base64"
	"errors"
	"mime"
	"strings"

	"github.com/Esau21/Go-smpt-dte/internal/mailbox"
	"github.com/gin-gonic/gin"
	"golang.org/x/net/html/charset"
	"google.golang.org/api/gmail/v1"
)

// dteQuery encuentra los correos que traen un .json adjunto.
const dteQuery = "filename:json"

// maxDTESize es el tamaño máximo de un .json que se intenta leer como DTE.
// Un DTE real pesa unos pocos KB.
const maxDTESize = 5 << 20

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
		Fields("messages(id),nextPageToken").
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

// findJSONPart busca, entre las partes del correo, el primer adjunto .json.
func findJSONPart(part *gmail.MessagePart) *gmail.MessagePart {
	if part == nil {
		return nil
	}
	if strings.HasSuffix(strings.ToLower(part.Filename), ".json") && part.Body != nil {
		return part
	}
	for _, child := range part.Parts {
		if found := findJSONPart(child); found != nil {
			return found
		}
	}
	return nil
}

// decodeBody decodifica el contenido que entrega Gmail (base64 para URL,
// con o sin relleno).
func decodeBody(data string) ([]byte, error) {
	return base64.RawURLEncoding.DecodeString(strings.TrimRight(data, "="))
}

// headerOf devuelve una cabecera del correo, ya legible: los asuntos y
// nombres con tildes viajan codificados.
func headerOf(m *gmail.Message, name string) string {
	if m.Payload == nil {
		return ""
	}
	decoder := mime.WordDecoder{CharsetReader: charset.NewReaderLabel}
	for _, h := range m.Payload.Headers {
		if strings.EqualFold(h.Name, name) {
			if value, err := decoder.DecodeHeader(h.Value); err == nil {
				return value
			}
			return h.Value
		}
	}
	return ""
}

// FetchDTE devuelve el .json adjunto de un correo. Si el correo no trae
// ninguno, devuelve nil sin error.
//
// No descarga el correo completo: pide solo su estructura y después el
// adjunto .json. Así un correo con un PDF de varios MB cuesta unos pocos KB,
// que es lo que hace viable sincronizar cuentas con miles de facturas.
func (*GmailClient) FetchDTE(c *gin.Context, id string) (*mailbox.Message, error) {
	s, ok := sessionOf(c)
	if !ok {
		return nil, errNoSession
	}
	ctx := c.Request.Context()

	m, err := s.gmail.Users.Messages.Get("me", id).
		Format("full").
		Fields("id,payload").
		Context(ctx).Do()
	if err != nil {
		return nil, err
	}

	part := findJSONPart(m.Payload)
	if part == nil || part.Body.Size > maxDTESize {
		return nil, nil
	}

	data := part.Body.Data
	if data == "" && part.Body.AttachmentId != "" {
		attachment, err := s.gmail.Users.Messages.Attachments.
			Get("me", id, part.Body.AttachmentId).
			Context(ctx).Do()
		if err != nil {
			return nil, err
		}
		data = attachment.Data
	}
	content, err := decodeBody(data)
	if err != nil || len(content) == 0 {
		return nil, nil
	}

	return &mailbox.Message{
		ID:      m.Id,
		From:    headerOf(m, "From"),
		Subject: headerOf(m, "Subject"),
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
