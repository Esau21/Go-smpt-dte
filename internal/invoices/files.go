package invoices

import (
	"context"
	"mime"
	"net/http"
	"path"
	"strconv"
	"strings"

	"github.com/Esau21/Go-smpt-dte/internal/bucket"
	"github.com/Esau21/Go-smpt-dte/internal/store"
	"github.com/Esau21/Go-smpt-dte/internal/validations"
	"github.com/gin-gonic/gin"
)

// maxFileSize es el tamaño máximo de un archivo que se puede guardar.
const maxFileSize = 20 << 20 // 20 MB

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
func (h *Handlers) objectKey(ctx context.Context, owner, messageID, filename string) (string, error) {
	fecha, emisor, _ := h.Store.InvoiceOfMessage(ctx, owner, messageID)
	key := bucket.Key(owner, fecha, emisor, filename)

	taken, err := h.Store.KeyTaken(ctx, owner, key, messageID)
	if err != nil || !taken {
		return key, err
	}
	ext := path.Ext(filename)
	unique := strings.TrimSuffix(filename, ext) + " (" + messageID + ")" + ext
	return bucket.Key(owner, fecha, emisor, unique), nil
}

// SaveFile sube al bucket una copia de un adjunto, en una ruta ordenada,
// para conservarlo aunque el correo se borre.
func (h *Handlers) SaveFile(c *gin.Context) {
	owner, ok := h.owner(c)
	if !ok {
		return
	}
	ctx := c.Request.Context()

	var body struct {
		MessageID string `json:"messageId"`
		Index     *int   `json:"index"`
		// Folder es la carpeta elegida por el usuario. Si no viene, el
		// archivo se ordena solo: año, mes y emisor.
		Folder *string `json:"folder"`
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

	var key string
	if body.Folder == nil {
		key, err = h.objectKey(ctx, owner, attachment.MessageID, attachment.Filename)
	} else {
		folder, ok := bucket.CleanFolder(*body.Folder)
		if !ok {
			validations.Fail(c, 400, "invalid_folder")
			return
		}
		key, err = h.keyInFolder(ctx, owner, folder, attachment.Filename, func(candidate string) (bool, error) {
			return h.Store.KeyTaken(ctx, owner, candidate, attachment.MessageID)
		})
	}
	if err != nil {
		storageFailed(c, err)
		return
	}

	// Si este adjunto ya estaba guardado en otra carpeta, se quita de ahí:
	// guardar de nuevo lo cambia de lugar, no lo duplica.
	previous := ""
	if old, _ := h.Store.ListFiles(ctx, owner, attachment.MessageID); old != nil {
		for _, f := range old {
			if f.Filename == attachment.Filename && f.Path != key {
				previous = f.Path
			}
		}
	}

	if err := h.Bucket.Put(ctx, key, attachment.Content, contentTypeOf(attachment.Filename)); err != nil {
		storageFailed(c, err)
		return
	}

	saved, err := h.Store.SaveFile(ctx, owner, store.SavedFile{
		MessageID:   attachment.MessageID,
		Filename:    attachment.Filename,
		Size:        len(attachment.Content),
		MailFrom:    attachment.From,
		MailSubject: attachment.Subject,
		Path:        key,
	})
	if err != nil {
		storageFailed(c, err)
		return
	}
	if previous != "" {
		if err := h.Bucket.Delete(ctx, previous); err != nil {
			storageWarn("no se pudo borrar la copia anterior: "+previous, err)
		}
	}
	c.JSON(http.StatusCreated, saved)
}

// keyInFolder arma la ruta de un archivo dentro de una carpeta. Si otro
// archivo ya tiene ese nombre ahí, le agrega un número: "factura (2).pdf".
func (h *Handlers) keyInFolder(ctx context.Context, owner, folder, filename string, taken func(string) (bool, error)) (string, error) {
	ext := path.Ext(filename)
	base := strings.TrimSuffix(filename, ext)
	for n := 1; ; n++ {
		name := filename
		if n > 1 {
			name = base + " (" + strconv.Itoa(n) + ")" + ext
		}
		key := bucket.KeyIn(owner, folder, name)
		used, err := taken(key)
		if err != nil || !used || n > 50 {
			return key, err
		}
	}
}

// MoveFile cambia un archivo guardado a otra carpeta.
func (h *Handlers) MoveFile(c *gin.Context) {
	owner, ok := h.owner(c)
	if !ok {
		return
	}
	id, ok := fileID(c)
	if !ok {
		return
	}
	ctx := c.Request.Context()

	var body struct {
		Folder *string `json:"folder"`
	}
	if err := c.ShouldBindJSON(&body); err != nil || body.Folder == nil {
		validations.Fail(c, 400, "invalid_body")
		return
	}
	folder, valid := bucket.CleanFolder(*body.Folder)
	if !valid {
		validations.Fail(c, 400, "invalid_folder")
		return
	}

	file, err := h.Store.File(ctx, owner, id)
	if err == store.ErrNotFound {
		validations.Fail(c, 404, "file_not_found")
		return
	}
	if err != nil {
		storageFailed(c, err)
		return
	}

	key, err := h.keyInFolder(ctx, owner, folder, file.Filename, func(candidate string) (bool, error) {
		return h.Store.PathTaken(ctx, owner, candidate, id)
	})
	if err != nil {
		storageFailed(c, err)
		return
	}
	if key == file.Path {
		c.JSON(200, file)
		return
	}

	// En un bucket no existe "mover": se copia a la ruta nueva, se anota y
	// al final se borra la anterior. Si algo falla a medias, el archivo
	// sigue disponible en su lugar original.
	content, err := h.Bucket.Get(ctx, file.Path)
	if err == bucket.ErrNotFound {
		validations.Fail(c, 404, "file_not_found")
		return
	}
	if err == nil {
		err = h.Bucket.Put(ctx, key, content, contentTypeOf(file.Filename))
	}
	if err != nil {
		storageFailed(c, err)
		return
	}
	moved, err := h.Store.MoveFile(ctx, owner, id, key)
	if err != nil {
		storageFailed(c, err)
		return
	}
	if err := h.Bucket.Delete(ctx, file.Path); err != nil {
		storageWarn("no se pudo borrar la ruta anterior: "+file.Path, err)
	}
	c.JSON(200, moved)
}

// CreateFolder crea una carpeta vacía.
func (h *Handlers) CreateFolder(c *gin.Context) {
	owner, ok := h.owner(c)
	if !ok {
		return
	}
	var body struct {
		Path string `json:"path"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		validations.Fail(c, 400, "invalid_body")
		return
	}
	folder, valid := bucket.CleanFolder(body.Path)
	if !valid || folder == "" {
		validations.Fail(c, 400, "invalid_folder")
		return
	}
	if err := h.Store.AddFolder(c.Request.Context(), owner, folder); err != nil {
		storageFailed(c, err)
		return
	}
	c.JSON(http.StatusCreated, gin.H{"path": folder})
}

// DeleteFolder borra una carpeta creada por el usuario, si está vacía.
func (h *Handlers) DeleteFolder(c *gin.Context) {
	owner, ok := h.owner(c)
	if !ok {
		return
	}
	folder, valid := bucket.CleanFolder(c.Query("path"))
	if !valid || folder == "" {
		validations.Fail(c, 400, "invalid_folder")
		return
	}

	prefix := strings.TrimSuffix(bucket.KeyIn(owner, folder, "x"), "/x")
	err := h.Store.RemoveFolder(c.Request.Context(), owner, folder, prefix)
	switch err {
	case nil:
		c.Status(http.StatusNoContent)
	case store.ErrNotEmpty:
		validations.Fail(c, http.StatusConflict, "folder_not_empty")
	case store.ErrNotFound:
		validations.Fail(c, 404, "folder_not_found")
	default:
		storageFailed(c, err)
	}
}

// ListFiles lista los archivos guardados; con ?messageId= solo los de un correo.
func (h *Handlers) ListFiles(c *gin.Context) {
	owner, ok := h.owner(c)
	if !ok {
		return
	}
	files, err := h.Store.ListFiles(c.Request.Context(), owner, c.Query("messageId"))
	if err != nil {
		storageFailed(c, err)
		return
	}
	folders, err := h.Store.Folders(c.Request.Context(), owner)
	if err != nil {
		storageFailed(c, err)
		return
	}
	c.JSON(200, gin.H{"files": files, "folders": folders, "bucket": h.Bucket.Name(), "account": owner})
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

	file, err := h.Store.File(ctx, owner, id)
	if err == store.ErrNotFound {
		validations.Fail(c, 404, "file_not_found")
		return
	}
	if err != nil {
		storageFailed(c, err)
		return
	}

	content, err := h.Bucket.Get(ctx, file.Path)
	if err == bucket.ErrNotFound {
		validations.Fail(c, 404, "file_not_found")
		return
	}
	if err != nil {
		storageFailed(c, err)
		return
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
		storageFailed(c, err)
		return
	}
	if err := h.Bucket.Delete(ctx, key); err != nil {
		// El registro ya se borró: el usuario no lo ve más. El objeto
		// huérfano queda anotado para limpiarlo a mano.
		storageWarn("no se pudo borrar del bucket: "+key, err)
	}
	c.Status(http.StatusNoContent)
}
