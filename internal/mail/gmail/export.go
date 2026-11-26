package mail_gmail

import (
	"mime"
	"strings"

	"github.com/Esau21/Go-smpt-dte/internal/validations"
	"github.com/gin-gonic/gin"
	"github.com/jhillyerd/enmime/v2"
	"google.golang.org/api/gmail/v1"
)

// Este archivo expone lo que no depende de Gmail —cómo se convierte un
// correo ya descargado en la respuesta de la API— para que los demás
// proveedores (Outlook) respondan exactamente igual.

// EnvelopeJSON arma la respuesta de "ver un correo": cabeceras, texto, HTML
// con las imágenes incrustadas y la lista de adjuntos.
func EnvelopeJSON(id, threadID string, mail *enmime.Envelope) gin.H {
	result := fields(&gmail.Message{Id: id, ThreadId: threadID}, mail.GetHeader)
	result["to"] = mail.GetHeader("To")
	result["text"] = mail.Text
	result["html"] = inlineImages(mail)
	result["attachments"] = fileList(mail)
	return result
}

// FindDTE devuelve el contenido del .json adjunto, si el correo trae uno.
func FindDTE(mail *enmime.Envelope) ([]byte, bool) {
	return findDTE(mail)
}

// Files lista los adjuntos del correo, en el orden que usa la API.
func Files(mail *enmime.Envelope) []*enmime.Part {
	return files(mail)
}

// ServeAttachment entrega el adjunto número `index`. Solo los PDF se
// muestran en el navegador; todo lo demás se descarga.
func ServeAttachment(c *gin.Context, mail *enmime.Envelope, index int) {
	parts := files(mail)
	if index < 0 || index >= len(parts) {
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

// HasSession dice si esta petición trae una sesión de Gmail vigente.
func (gc *GmailClient) HasSession(c *gin.Context) bool {
	id, _ := c.Cookie("gmail_session")
	return id != "" && gc.sessions.Get(id) != nil
}

// Providers lista los proveedores con los que se puede iniciar sesión.
func (*GmailClient) Providers() []string {
	return []string{"gmail"}
}
