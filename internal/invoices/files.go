package invoices

import (
	"context"
	"log"
	"mime"
	"net/http"
	"path"
	"strconv"
	"strings"

	"github.com/Esau21/Go-smpt-dte/internal/bucket"
	"github.com/Esau21/Go-smpt-dte/internal/mailbox"
	"github.com/Esau21/Go-smpt-dte/internal/store"
	"github.com/Esau21/Go-smpt-dte/internal/validations"
	"github.com/gin-gonic/gin"
)

// maxFileSize es el tamaño máximo de un archivo que se puede guardar.
const maxFileSize = 20 << 20 // 20 MB

// account devuelve la cuenta con sesión, o responde el error y devuelve false.
func (h *Handlers) account(c *gin.Context) (mailbox.Account, bool) {
	if h.Store == nil {
		validations.Fail(c, http.StatusServiceUnavailable, "database_unavailable")
		return mailbox.Account{}, false
	}
	account, ok := h.Mail.Account(c)
	if !ok {
		validations.Fail(c, http.StatusUnauthorized, "login_required")
	}
	return account, ok
}

func contentTypeOf(filename string) string {
	if t := mime.TypeByExtension(strings.ToLower(path.Ext(filename))); t != "" {
		return t
	}
	return "application/octet-stream"
}

// objectKey decide en qué carpeta del bucket va un archivo: la de la factura
// que llegó en ese correo (año, mes y emisor), o "sin-clasificar". Si otro
// correo ya ocupó esa misma ruta, agrega el identificador del correo al
// nombre para no pisar el archivo anterior.
func (h *Handlers) objectKey(ctx context.Context, account mailbox.Account, messageID, filename string) (string, error) {
	fecha, emisor, _ := h.Store.InvoiceOfMessage(ctx, account.Owner(), messageID)
	key := bucket.Key(account.Email, fecha, emisor, filename)

	taken, err := h.Store.KeyTaken(ctx, account.Owner(), key, messageID)
	if err != nil || !taken {
		return key, err
	}
	ext := path.Ext(filename)
	unique := strings.TrimSuffix(filename, ext) + " (" + messageID + ")" + ext
	return bucket.Key(account.Email, fecha, emisor, unique), nil
}

// SaveFile guarda en la nube una copia de un adjunto, para conservarlo
// aunque el correo se borre. Con bucket configurado el archivo va al bucket,
// en una ruta ordenada; sin bucket, queda en la base de datos.
func (h *Handlers) SaveFile(c *gin.Context) {
	account, ok := h.account(c)
	if !ok {
		return
	}
	owner := account.Owner()
	ctx := c.Request.Context()

	var body struct {
		MessageID string `json:"messageId"`
		Index     *int   `json:"index"`
	}
	if err := c.ShouldBindJSON(&body); err != nil || body.MessageID == "" || body.Index == nil {
		validations.Fail(c, 400, "invalid_body")
		return
	}

	attachment, err := h.Mail.FetchAttachment(c, body.MessageID, *body.Index)
	if err != nil {
		status := h.Mail.ErrorStatus(err)
		validations.Fail(c, status, http.StatusText(status))
		return
	}
	if attachment == nil {
		validations.Fail(c, 404, "attachment_not_found")
		return
	}
	if len(attachment.Content) > maxFileSize {
		validations.Fail(c, http.StatusRequestEntityTooLarge, "file_too_large")
		return
	}

	file := store.SavedFile{
		MessageID:   attachment.MessageID,
		Filename:    attachment.Filename,
		MailFrom:    attachment.From,
		MailSubject: attachment.Subject,
	}
	if h.Bucket != nil {
		file.Path, err = h.objectKey(ctx, account, file.MessageID, file.Filename)
		if err != nil {
			validations.Fail(c, 500, "database_error")
			return
		}
		if err := h.Bucket.Put(ctx, file.Path, attachment.Content, contentTypeOf(file.Filename)); err != nil {
			log.Println("Error: no se pudo subir al bucket:", err)
			validations.Fail(c, http.StatusBadGateway, "bucket_error")
			return
		}
	}

	saved, err := h.Store.SaveFile(ctx, owner, file, attachment.Content)
	if err != nil {
		validations.Fail(c, 500, "database_error")
		return
	}
	c.JSON(http.StatusCreated, saved)
}

// migrateLegacy pasa al bucket los archivos de la cuenta que se guardaron
// en la base de datos antes de que existiera el bucket. Si algo falla, el
// archivo sigue donde estaba y se reintenta la próxima vez.
func (h *Handlers) migrateLegacy(ctx context.Context, account mailbox.Account) {
	if h.Bucket == nil {
		return
	}
	owner := account.Owner()
	ids, err := h.Store.LegacyFiles(ctx, owner)
	if err != nil {
		return
	}
	for _, id := range ids {
		file, content, err := h.Store.FileContent(ctx, owner, id)
		if err != nil || content == nil {
			continue
		}
		key, err := h.objectKey(ctx, account, file.MessageID, file.Filename)
		if err != nil {
			continue
		}
		if err := h.Bucket.Put(ctx, key, content, contentTypeOf(file.Filename)); err != nil {
			log.Println("Warning: no se pudo pasar un archivo al bucket:", err)
			continue
		}
		_ = h.Store.MoveToBucket(ctx, owner, id, key)
	}
}

// ListFiles lista los archivos guardados; con ?messageId= solo los de un correo.
func (h *Handlers) ListFiles(c *gin.Context) {
	account, ok := h.account(c)
	if !ok {
		return
	}
	ctx := c.Request.Context()
	h.migrateLegacy(ctx, account)

	files, err := h.Store.ListFiles(ctx, account.Owner(), c.Query("messageId"))
	if err != nil {
		validations.Fail(c, 500, "database_error")
		return
	}

	name := ""
	if h.Bucket != nil {
		name = h.Bucket.Name()
	}
	c.JSON(200, gin.H{"files": files, "bucket": name, "account": account.Email})
}

func fileID(c *gin.Context) (int64, bool) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		validations.Fail(c, 400, "invalid_id")
		return 0, false
	}
	return id, true
}

// DownloadFile entrega un archivo guardado. Igual que con los adjuntos del
// correo, solo los PDF se muestran en el navegador; lo demás se descarga.
func (h *Handlers) DownloadFile(c *gin.Context) {
	owner, ok := h.owner(c)
	if !ok {
		return
	}
	id, ok := fileID(c)
	if !ok {
		return
	}
	ctx := c.Request.Context()

	file, content, err := h.Store.FileContent(ctx, owner, id)
	if err == store.ErrNotFound {
		validations.Fail(c, 404, "file_not_found")
		return
	}
	if err != nil {
		validations.Fail(c, 500, "database_error")
		return
	}

	if file.Path != "" {
		if h.Bucket == nil {
			validations.Fail(c, http.StatusServiceUnavailable, "bucket_unavailable")
			return
		}
		content, err = h.Bucket.Get(ctx, file.Path)
		if err == bucket.ErrNotFound {
			validations.Fail(c, 404, "file_not_found")
			return
		}
		if err != nil {
			log.Println("Error: no se pudo leer del bucket:", err)
			validations.Fail(c, http.StatusBadGateway, "bucket_error")
			return
		}
	}

	contentType, disposition := "application/octet-stream", "attachment"
	if strings.HasSuffix(strings.ToLower(file.Filename), ".pdf") && c.Query("download") == "" {
		contentType, disposition = "application/pdf", "inline"
	}
	c.Header("Content-Disposition", mime.FormatMediaType(disposition, map[string]string{"filename": file.Filename}))
	c.Header("X-Content-Type-Options", "nosniff")
	c.Data(200, contentType, content)
}

func (h *Handlers) DeleteFile(c *gin.Context) {
	owner, ok := h.owner(c)
	if !ok {
		return
	}
	id, ok := fileID(c)
	if !ok {
		return
	}
	ctx := c.Request.Context()

	key, err := h.Store.DeleteFile(ctx, owner, id)
	if err == store.ErrNotFound {
		validations.Fail(c, 404, "file_not_found")
		return
	}
	if err != nil {
		validations.Fail(c, 500, "database_error")
		return
	}
	if key != "" && h.Bucket != nil {
		if err := h.Bucket.Delete(ctx, key); err != nil {
			// El registro ya se borró: el usuario no lo ve más. El objeto
			// huérfano queda anotado para limpiarlo a mano.
			log.Println("Warning: no se pudo borrar del bucket:", key, err)
		}
	}
	c.Status(http.StatusNoContent)
}
