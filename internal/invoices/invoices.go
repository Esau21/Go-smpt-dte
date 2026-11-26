// Package invoices expone las facturas guardadas: sincronizarlas desde el
// correo, listarlas, resumirlas, anotarlas y exportarlas a Excel. También
// los archivos que el usuario guarda en la nube.
package invoices

import (
	"context"
	"log"
	"net/http"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Esau21/Go-smpt-dte/internal/dte"
	"github.com/Esau21/Go-smpt-dte/internal/mailbox"
	"github.com/Esau21/Go-smpt-dte/internal/store"
	"github.com/Esau21/Go-smpt-dte/internal/validations"
	"github.com/gin-gonic/gin"
	"golang.org/x/sync/errgroup"
)

// Mailbox es lo que este paquete necesita del proveedor de correo.
type Mailbox interface {
	Account(c *gin.Context) (mailbox.Account, bool)
	ListDTEMessages(c *gin.Context, pageToken string, limit int) (ids []string, next string, err error)
	FetchDTE(c *gin.Context, id string) (*mailbox.Message, error)
	FetchAttachment(c *gin.Context, id string, index int) (*mailbox.Attachment, error)
	ErrorStatus(err error) int
}

// Files es el bucket donde vive todo: los archivos y los datos de cada cuenta.
type Files interface {
	Name() string
	Put(ctx context.Context, key string, content []byte, contentType string) error
	Get(ctx context.Context, key string) ([]byte, error)
	Delete(ctx context.Context, key string) error
}

type Handlers struct {
	// Bucket y Store son nil si el bucket no está disponible; entonces
	// estas rutas responden 503 y el resto de la API sigue funcionando.
	Bucket Files
	Store  *store.Store
	Mail   Mailbox
}

// New arma los manejadores sobre un bucket (nil si no hay ninguno disponible).
func New(files Files, mail Mailbox) *Handlers {
	h := &Handlers{Mail: mail}
	if files != nil {
		h.Bucket = files
		h.Store = store.New(files)
	}
	return h
}

// syncPage es cuántos correos se revisan por llamada a /sync, y
// syncWorkers cuántos se piden a la vez. De cada correo solo se baja el
// .json (unos KB), así que una página entera tarda pocos segundos.
const (
	syncPage    = 50
	syncWorkers = 8
)

// elSalvador es la zona horaria del país (UTC-6, sin horario de verano).
var elSalvador = time.FixedZone("CST", -6*60*60)

var dateRE = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}$`)

// owner devuelve la carpeta de la cuenta con sesión (su correo), o responde
// el error y devuelve false.
func (h *Handlers) owner(c *gin.Context) (string, bool) {
	if h.Store == nil {
		validations.Fail(c, http.StatusServiceUnavailable, "storage_unavailable")
		return "", false
	}
	account, ok := h.Mail.Account(c)
	if !ok {
		validations.Fail(c, http.StatusUnauthorized, "login_required")
		return "", false
	}
	return account.Email, true
}

// storageFailed responde un error del bucket y lo deja en el registro.
func storageFailed(c *gin.Context, err error) {
	log.Println("Error: fallo del bucket:", err)
	validations.Fail(c, http.StatusBadGateway, "storage_error")
}

func storageWarn(message string, err error) {
	log.Println("Warning:", message, err)
}

// Sync revisa una página de correos con .json adjunto y guarda los DTE
// nuevos. El cliente la llama varias veces, pasando nextPageToken, hasta
// que la respuesta trae done=true.
func (h *Handlers) Sync(c *gin.Context) {
	owner, ok := h.owner(c)
	if !ok {
		return
	}
	ctx := c.Request.Context()

	ids, next, err := h.Mail.ListDTEMessages(c, c.Query("pageToken"), syncPage)
	if err != nil {
		status := h.Mail.ErrorStatus(err)
		validations.Fail(c, status, http.StatusText(status))
		return
	}

	known, err := h.Store.Known(ctx, owner, ids)
	if err != nil {
		storageFailed(c, err)
		return
	}

	var (
		mu      sync.Mutex
		found   []store.Invoice
		scanned []string
		skipped int
	)
	group, _ := errgroup.WithContext(ctx)
	group.SetLimit(syncWorkers)

	for _, id := range ids {
		if known[id] {
			continue
		}
		group.Go(func() error {
			message, err := h.Mail.FetchDTE(c, id)
			if err != nil {
				// Un correo borrado mientras se sincroniza no detiene el resto.
				if h.Mail.ErrorStatus(err) == 404 {
					return nil
				}
				return err
			}

			invoice, isDTE := toInvoice(message)
			mu.Lock()
			defer mu.Unlock()
			if isDTE {
				found = append(found, invoice)
			} else {
				skipped++
			}
			scanned = append(scanned, id)
			return nil
		})
	}

	if err := group.Wait(); err != nil {
		status := h.Mail.ErrorStatus(err)
		validations.Fail(c, status, http.StatusText(status))
		return
	}

	// Toda la página se guarda de una vez.
	imported := 0
	if len(scanned) > 0 {
		if imported, err = h.Store.SaveSync(ctx, owner, found, scanned); err != nil {
			storageFailed(c, err)
			return
		}
	}

	// Los correos vienen del más reciente al más antiguo: si toda la página
	// ya estaba revisada, lo que sigue también lo está (salvo que se pida todo).
	allKnown := len(ids) > 0 && len(known) == len(ids)
	done := next == "" || (allKnown && c.Query("full") == "")

	c.JSON(200, gin.H{
		"scanned":       len(ids),
		"imported":      imported,
		"skipped":       skipped,
		"nextPageToken": next,
		"done":          done,
	})
}

// toInvoice convierte el .json de un correo en una factura. Devuelve false
// si el adjunto no es un DTE.
func toInvoice(m *mailbox.Message) (store.Invoice, bool) {
	if m == nil {
		return store.Invoice{}, false
	}
	doc, err := dte.Parse(m.DTE)
	if err != nil || doc.Identificacion.CodigoGeneracion == "" {
		return store.Invoice{}, false
	}

	id := doc.Identificacion
	amounts := doc.Amounts()
	receptor := doc.ReceptorParty()

	fecha := id.FecEmi
	if !dateRE.MatchString(fecha) {
		fecha = ""
	}
	moneda := id.TipoMoneda
	if moneda == "" {
		moneda = "USD"
	}

	return store.Invoice{
		CodigoGeneracion: strings.ToUpper(id.CodigoGeneracion),
		NumeroControl:    id.NumeroControl,
		TipoDte:          id.TipoDte,
		Ambiente:         id.Ambiente,
		Fecha:            fecha,
		Hora:             id.HorEmi,
		Moneda:           moneda,
		EmisorNIT:        doc.Emisor.NIT,
		EmisorNRC:        doc.Emisor.NRC,
		EmisorNombre:     doc.Emisor.Nombre,
		ReceptorDoc:      receptor.Documento,
		ReceptorNRC:      receptor.NRC,
		ReceptorNombre:   receptor.Nombre,
		NoSujeto:         amounts.NoSujeto,
		Exento:           amounts.Exento,
		Gravado:          amounts.Gravado,
		IVA:              amounts.IVA,
		IVAPercibido:     amounts.IVAPercibido,
		IVARetenido:      amounts.IVARetenido,
		Total:            amounts.Total,
		Sello:            doc.SelloRecibido,
		MessageID:        m.ID,
		MailFrom:         m.From,
		MailSubject:      m.Subject,
		Status:           "pendiente",
	}, true
}

// filter lee los filtros de la URL. Responde 400 y devuelve false si
// alguno no es válido.
func filter(c *gin.Context) (store.Filter, bool) {
	f := store.Filter{
		From:   c.Query("from"),
		To:     c.Query("to"),
		Status: c.Query("status"),
		Tipo:   c.Query("tipo"),
		Search: strings.TrimSpace(c.Query("q")),
		Sort:   c.Query("sort"),
	}
	if !slices.Contains(store.Sorts, f.Sort) {
		validations.Fail(c, 400, "invalid_sort")
		return f, false
	}
	for _, date := range []string{f.From, f.To} {
		if date != "" && !dateRE.MatchString(date) {
			validations.Fail(c, 400, "invalid_date")
			return f, false
		}
	}
	if f.Status != "" && !slices.Contains(store.Statuses, f.Status) {
		validations.Fail(c, 400, "invalid_status")
		return f, false
	}
	return f, true
}

func (h *Handlers) List(c *gin.Context) {
	owner, ok := h.owner(c)
	if !ok {
		return
	}
	f, ok := filter(c)
	if !ok {
		return
	}

	limit, err := strconv.Atoi(c.DefaultQuery("limit", "50"))
	if err != nil || limit < 1 || limit > 200 {
		validations.Fail(c, 400, "limit_must_be_1_to_200")
		return
	}
	offset, err := strconv.Atoi(c.DefaultQuery("offset", "0"))
	if err != nil || offset < 0 {
		validations.Fail(c, 400, "invalid_offset")
		return
	}
	f.Limit, f.Offset = limit, offset

	items, totals, err := h.Store.List(c.Request.Context(), owner, f)
	if err != nil {
		storageFailed(c, err)
		return
	}
	byStatus, err := h.Store.StatusCounts(c.Request.Context(), owner, f)
	if err != nil {
		storageFailed(c, err)
		return
	}
	c.JSON(200, gin.H{"items": items, "totals": totals, "byStatus": byStatus})
}

// UpdateMany cambia el estado de varias facturas a la vez.
func (h *Handlers) UpdateMany(c *gin.Context) {
	owner, ok := h.owner(c)
	if !ok {
		return
	}
	var body struct {
		IDs    []int64 `json:"ids"`
		Status string  `json:"status"`
	}
	if err := c.ShouldBindJSON(&body); err != nil || len(body.IDs) == 0 || len(body.IDs) > 5000 {
		validations.Fail(c, 400, "invalid_body")
		return
	}
	if !slices.Contains(store.Statuses, body.Status) {
		validations.Fail(c, 400, "invalid_status")
		return
	}

	updated, err := h.Store.UpdateMany(c.Request.Context(), owner, body.IDs, body.Status)
	if err != nil {
		storageFailed(c, err)
		return
	}
	c.JSON(200, gin.H{"updated": updated})
}

// Summary resume los últimos 12 meses (incluido el actual).
func (h *Handlers) Summary(c *gin.Context) {
	owner, ok := h.owner(c)
	if !ok {
		return
	}

	now := time.Now().In(elSalvador)
	since := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, elSalvador).AddDate(0, -11, 0)

	summary, err := h.Store.Summary(c.Request.Context(), owner, since.Format("2006-01-02"))
	if err != nil {
		storageFailed(c, err)
		return
	}
	c.JSON(200, gin.H{
		"since":      since.Format("2006-01"),
		"thisMonth":  now.Format("2006-01"),
		"months":     summary.Months,
		"issuers":    summary.Issuers,
		"byStatus":       summary.ByStatus,
		"categories":     summary.Categories,
		"recent":         summary.Recent,
		"types":          summary.Types,
		"categoryTotals": summary.CategoryTotals,
		"pending":        summary.Pending,
	})
}

func (h *Handlers) Update(c *gin.Context) {
	owner, ok := h.owner(c)
	if !ok {
		return
	}
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		validations.Fail(c, 400, "invalid_id")
		return
	}

	var patch store.Patch
	if err := c.ShouldBindJSON(&patch); err != nil {
		validations.Fail(c, 400, "invalid_body")
		return
	}
	if patch.Status != nil && !slices.Contains(store.Statuses, *patch.Status) {
		validations.Fail(c, 400, "invalid_status")
		return
	}
	if patch.Category != nil {
		value := strings.TrimSpace(*patch.Category)
		if len(value) > 60 {
			validations.Fail(c, 400, "category_too_long")
			return
		}
		patch.Category = &value
	}
	if patch.Notes != nil && len(*patch.Notes) > 2000 {
		validations.Fail(c, 400, "notes_too_long")
		return
	}

	invoice, err := h.Store.Update(c.Request.Context(), owner, id, patch)
	if err == store.ErrNotFound {
		validations.Fail(c, 404, "invoice_not_found")
		return
	}
	if err != nil {
		storageFailed(c, err)
		return
	}
	c.JSON(200, invoice)
}

// Export descarga las facturas filtradas como Excel. kind=compras genera
// el libro de compras; cualquier otro valor, el detalle completo.
func (h *Handlers) Export(c *gin.Context) {
	owner, ok := h.owner(c)
	if !ok {
		return
	}
	f, ok := filter(c)
	if !ok {
		return
	}

	items, _, err := h.Store.List(c.Request.Context(), owner, f)
	if err != nil {
		storageFailed(c, err)
		return
	}
	// Los libros se llevan en orden cronológico.
	slices.Reverse(items)

	period := periodLabel(f.From, f.To)
	var (
		content []byte
		name    string
	)
	if c.Query("kind") == "compras" {
		content, err = purchasesBook(items, period)
		name = "libro-de-compras"
	} else {
		content, err = detailBook(items, period)
		name = "facturas"
	}
	if err != nil {
		validations.Fail(c, 500, "export_failed")
		return
	}

	if f.From != "" || f.To != "" {
		name += "_" + strings.Trim(f.From+"_"+f.To, "_")
	}
	c.Header("Content-Disposition", `attachment; filename="`+name+`.xlsx"`)
	c.Data(200, "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet", content)
}

func periodLabel(from, to string) string {
	switch {
	case from != "" && to != "":
		return "Del " + from + " al " + to
	case from != "":
		return "Desde " + from
	case to != "":
		return "Hasta " + to
	}
	return "Todas las fechas"
}
