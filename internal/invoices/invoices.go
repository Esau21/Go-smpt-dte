// Package invoices expone las facturas guardadas: sincronizarlas desde el
// correo, listarlas, resumirlas, anotarlas y exportarlas a Excel.
package invoices

import (
	"context"
	"net/http"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"
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

// Files es el almacenamiento de archivos (un bucket).
type Files interface {
	Name() string
	Put(ctx context.Context, key string, content []byte, contentType string) error
	Get(ctx context.Context, key string) ([]byte, error)
	Delete(ctx context.Context, key string) error
}

type Handlers struct {
	Store *store.Store // nil si la base de datos no está configurada
	// Bucket es donde se guardan los archivos de "la nube"; nil si no hay
	// bucket configurado (entonces quedan en la base de datos).
	Bucket Files
	Mail  Mailbox
}

// syncPage es cuántos correos se revisan por llamada a /sync.
const syncPage = 20

// elSalvador es la zona horaria del país (UTC-6, sin horario de verano).
var elSalvador = time.FixedZone("CST", -6*60*60)

var dateRE = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}$`)

// owner devuelve la cuenta con sesión, o responde el error y devuelve false.
func (h *Handlers) owner(c *gin.Context) (string, bool) {
	if h.Store == nil {
		validations.Fail(c, http.StatusServiceUnavailable, "database_unavailable")
		return "", false
	}
	account, ok := h.Mail.Account(c)
	if !ok {
		validations.Fail(c, http.StatusUnauthorized, "login_required")
		return "", false
	}
	return account.Owner(), true
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
		validations.Fail(c, 500, "database_error")
		return
	}

	var imported, skipped atomic.Int32
	group, _ := errgroup.WithContext(ctx)
	group.SetLimit(4)

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

			if invoice, ok := toInvoice(message); ok {
				isNew, err := h.Store.Upsert(ctx, owner, invoice)
				if err != nil {
					return err
				}
				if isNew {
					imported.Add(1)
				}
			} else {
				skipped.Add(1)
			}
			return h.Store.MarkScanned(ctx, owner, id)
		})
	}

	if err := group.Wait(); err != nil {
		status := h.Mail.ErrorStatus(err)
		validations.Fail(c, status, http.StatusText(status))
		return
	}

	// Los correos vienen del más reciente al más antiguo: si toda la página
	// ya estaba revisada, lo que sigue también lo está (salvo que se pida todo).
	allKnown := len(ids) > 0 && len(known) == len(ids)
	done := next == "" || (allKnown && c.Query("full") == "")

	c.JSON(200, gin.H{
		"scanned":       len(ids),
		"imported":      imported.Load(),
		"skipped":       skipped.Load(),
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
		validations.Fail(c, 500, "database_error")
		return
	}
	c.JSON(200, gin.H{"items": items, "totals": totals})
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
		validations.Fail(c, 500, "database_error")
		return
	}
	c.JSON(200, gin.H{
		"since":      since.Format("2006-01"),
		"thisMonth":  now.Format("2006-01"),
		"months":     summary.Months,
		"issuers":    summary.Issuers,
		"byStatus":   summary.ByStatus,
		"categories": summary.Categories,
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
		validations.Fail(c, 500, "database_error")
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
		validations.Fail(c, 500, "database_error")
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
