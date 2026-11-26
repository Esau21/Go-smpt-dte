package mail_gmail

import (
	"encoding/base64"
	"mime"
	"net/http"
	"path"
	"strconv"
	"strings"

	"github.com/Esau21/Go-smpt-dte/internal/validations"
	"github.com/gin-gonic/gin"
	"github.com/jhillyerd/enmime/v2"
)

// imageType devuelve el tipo de imagen de una parte del correo, o "" si no
// es una imagen. Algunos emisores mandan el logo como application/octet-stream,
// por eso también se mira la extensión y el contenido.
func imageType(p *enmime.Part) string {
	if strings.HasPrefix(p.ContentType, "image/") {
		return p.ContentType
	}
	if t := mime.TypeByExtension(strings.ToLower(path.Ext(p.FileName))); strings.HasPrefix(t, "image/") {
		return t
	}
	if t := http.DetectContentType(p.Content); strings.HasPrefix(t, "image/") {
		return t
	}
	return ""
}

// files lista los archivos adjuntos del correo (el .json, el .pdf, etc.).
// Las imágenes incrustadas en el cuerpo (logos, firmas) no cuentan como
// adjuntos. El orden es estable: el índice identifica al archivo.
func files(mail *enmime.Envelope) []*enmime.Part {
	var out []*enmime.Part
	out = append(out, mail.Attachments...)
	for _, parts := range [][]*enmime.Part{mail.Inlines, mail.OtherParts} {
		for _, p := range parts {
			if p.FileName == "" || (p.ContentID != "" && imageType(p) != "") {
				continue
			}
			out = append(out, p)
		}
	}
	return out
}

func fileList(mail *enmime.Envelope) []gin.H {
	list := []gin.H{}
	for i, p := range files(mail) {
		list = append(list, gin.H{
			"index":       i,
			"filename":    p.FileName,
			"contentType": p.ContentType,
			"size":        len(p.Content),
		})
	}
	return list
}

// GetAttachment entrega un adjunto del correo. Solo los PDF se muestran en el
// navegador; todo lo demás se descarga, para que un adjunto malicioso (por
// ejemplo un .html) nunca se ejecute dentro de la aplicación.
func (*GmailClient) GetAttachment(c *gin.Context) {
	value, _ := c.Get(sessionContextKey)
	s, ok := value.(session)
	if !ok {
		c.Abort()
		validations.Fail(c, http.StatusUnauthorized, "login_required")
		return
	}

	index, err := strconv.Atoi(c.Param("index"))
	if err != nil || index < 0 {
		validations.Fail(c, 400, "invalid_attachment")
		return
	}

	m, err := s.gmail.Users.Messages.Get("me", c.Param("id")).
		Format("raw").Fields("id,raw").
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

	parts := files(mail)
	if index >= len(parts) {
		validations.Fail(c, 404, "attachment_not_found")
		return
	}
	part := parts[index]

	contentType, disposition := "application/octet-stream", "attachment"
	if strings.HasSuffix(strings.ToLower(part.FileName), ".pdf") && c.Query("download") == "" {
		contentType, disposition = "application/pdf", "inline"
	}

	c.Header("Content-Disposition", mime.FormatMediaType(disposition, map[string]string{"filename": part.FileName}))
	c.Header("X-Content-Type-Options", "nosniff")
	c.Header("Cache-Control", "private, max-age=300")
	c.Data(200, contentType, part.Content)
}
