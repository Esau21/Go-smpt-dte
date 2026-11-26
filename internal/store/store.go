// Package store guarda los datos de cada cuenta —las facturas leídas del
// correo, lo que el usuario anota sobre ellas y el registro de los archivos
// guardados— como un documento JSON dentro del bucket:
//
//	cuenta/_bookeep/datos.json.gz
//
// Va comprimido: el documento de una cuenta con 20 000 facturas pesa unos
// 10 MB en texto y cerca de 1 MB comprimido, y se sube entero en cada cambio.
//
// No hay base de datos. Cada lectura trae el documento y cada cambio lo
// vuelve a escribir completo, lo que alcanza de sobra para los miles de
// facturas de una cuenta. Los cambios de una misma cuenta se hacen de uno en
// uno dentro de este proceso; si algún día corren varias copias de la API a
// la vez, habrá que añadir control de versiones al escribir.
package store

import (
	"bytes"
	"cmp"
	"compress/gzip"
	"context"
	"encoding/json"
	"io"
	"errors"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/Esau21/Go-smpt-dte/internal/bucket"
)

// Blob es lo que el almacén necesita del bucket.
type Blob interface {
	Put(ctx context.Context, key string, content []byte, contentType string) error
	Get(ctx context.Context, key string) ([]byte, error)
	Delete(ctx context.Context, key string) error
}

// Statuses son los estados que el usuario puede asignar a una factura.
var Statuses = []string{"pendiente", "revisada", "pagada"}

type Invoice struct {
	ID               int64   `json:"id"`
	CodigoGeneracion string  `json:"codigoGeneracion"`
	NumeroControl    string  `json:"numeroControl"`
	TipoDte          string  `json:"tipoDte"`
	Ambiente         string  `json:"ambiente"`
	Fecha            string  `json:"fecha"`
	Hora             string  `json:"hora"`
	Moneda           string  `json:"moneda"`
	EmisorNIT        string  `json:"emisorNit"`
	EmisorNRC        string  `json:"emisorNrc"`
	EmisorNombre     string  `json:"emisorNombre"`
	ReceptorDoc      string  `json:"receptorDoc"`
	ReceptorNRC      string  `json:"receptorNrc"`
	ReceptorNombre   string  `json:"receptorNombre"`
	NoSujeto         float64 `json:"noSujeto"`
	Exento           float64 `json:"exento"`
	Gravado          float64 `json:"gravado"`
	IVA              float64 `json:"iva"`
	IVAPercibido     float64 `json:"ivaPercibido"`
	IVARetenido      float64 `json:"ivaRetenido"`
	Total            float64 `json:"total"`
	Sello            string  `json:"sello"`
	MessageID        string  `json:"messageId"`
	MailFrom         string  `json:"mailFrom"`
	MailSubject      string  `json:"mailSubject"`
	Status           string  `json:"status"`
	Category         string  `json:"category"`
	Notes            string  `json:"notes"`
}

// SavedFile es el registro de un archivo guardado en el bucket.
type SavedFile struct {
	ID          int64     `json:"id"`
	MessageID   string    `json:"messageId"`
	Filename    string    `json:"filename"`
	Size        int       `json:"size"`
	MailFrom    string    `json:"mailFrom"`
	MailSubject string    `json:"mailSubject"`
	CreatedAt   time.Time `json:"createdAt"`
	// Path es la ruta del archivo dentro del bucket.
	Path string `json:"path"`
}

// document es todo lo que se guarda de una cuenta.
type document struct {
	NextInvoiceID int64       `json:"nextInvoiceId"`
	NextFileID    int64       `json:"nextFileId"`
	Invoices      []Invoice   `json:"invoices"`
	Files         []SavedFile `json:"files"`
	// Scanned son los correos ya revisados, traigan o no un DTE, para no
	// volver a descargarlos.
	Scanned []string `json:"scanned"`
	// Folders son las carpetas que el usuario creó a mano. Se guardan para
	// que existan aunque estén vacías: en un bucket una carpeta solo existe
	// mientras tenga archivos.
	Folders []string `json:"folders"`
}

type Store struct {
	blob Blob

	mu    sync.Mutex
	locks map[string]*sync.Mutex
	// cache guarda el último documento leído de cada cuenta. Sin esto, cada
	// pantalla volvería a bajar y a interpretar el documento entero, que
	// para una cuenta con miles de facturas pesa varios MB.
	cache map[string]cached
}

type cached struct {
	doc *document
	at  time.Time
}

// cacheTTL es cuánto se confía en la copia en memoria. Es corto a propósito:
// si algún día corren varias copias de la API, limita cuánto puede tardar
// una en ver lo que guardó otra.
const cacheTTL = 2 * time.Minute

func New(blob Blob) *Store {
	return &Store{blob: blob, locks: map[string]*sync.Mutex{}, cache: map[string]cached{}}
}

func (s *Store) remember(account string, doc *document) {
	s.mu.Lock()
	s.cache[account] = cached{doc: doc, at: time.Now()}
	s.mu.Unlock()
}

func (s *Store) forget(account string) {
	s.mu.Lock()
	delete(s.cache, account)
	s.mu.Unlock()
}

// view devuelve los datos de la cuenta para consultarlos. El documento que
// devuelve es compartido: quien lo recibe solo puede leerlo.
func (s *Store) view(ctx context.Context, account string) (*document, error) {
	s.mu.Lock()
	entry, ok := s.cache[account]
	s.mu.Unlock()
	if ok && time.Since(entry.at) < cacheTTL {
		return entry.doc, nil
	}

	doc, err := s.load(ctx, account)
	if err != nil {
		return nil, err
	}
	s.remember(account, doc)
	return doc, nil
}

func (s *Store) lock(account string) *sync.Mutex {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.locks[account] == nil {
		s.locks[account] = &sync.Mutex{}
	}
	return s.locks[account]
}

func (s *Store) load(ctx context.Context, account string) (*document, error) {
	doc := &document{NextInvoiceID: 1, NextFileID: 1}
	content, err := s.blob.Get(ctx, bucket.DataKey(account))
	if errors.Is(err, bucket.ErrNotFound) {
		// Cuentas guardadas antes de que el documento se comprimiera.
		content, err = s.blob.Get(ctx, bucket.LegacyDataKey(account))
		if errors.Is(err, bucket.ErrNotFound) {
			return doc, nil
		}
	}
	if err != nil {
		return nil, err
	}

	// Comprimido empieza con los bytes 0x1f 0x8b; si no, es JSON tal cual.
	if len(content) > 2 && content[0] == 0x1f && content[1] == 0x8b {
		reader, err := gzip.NewReader(bytes.NewReader(content))
		if err != nil {
			return nil, err
		}
		if content, err = io.ReadAll(reader); err != nil {
			return nil, err
		}
	}
	if err := json.Unmarshal(content, doc); err != nil {
		return nil, err
	}
	return doc, nil
}

// update carga los datos de la cuenta, aplica el cambio y los guarda. Si
// `change` devuelve un error, no se guarda nada.
func (s *Store) update(ctx context.Context, account string, change func(*document) error) error {
	lock := s.lock(account)
	lock.Lock()
	defer lock.Unlock()

	doc, err := s.load(ctx, account)
	if err != nil {
		return err
	}
	if err := change(doc); err != nil {
		return err
	}
	var packed bytes.Buffer
	writer := gzip.NewWriter(&packed)
	if err := json.NewEncoder(writer).Encode(doc); err != nil {
		return err
	}
	if err := writer.Close(); err != nil {
		return err
	}
	if err := s.blob.Put(ctx, bucket.DataKey(account), packed.Bytes(), "application/gzip"); err != nil {
		// No se sabe qué quedó guardado: mejor volver a leerlo.
		s.forget(account)
		return err
	}
	// `doc` se acaba de leer del bucket y nadie más lo tiene: puede pasar a
	// ser la copia compartida de solo lectura.
	s.remember(account, doc)
	// La copia sin comprimir de antes ya no hace falta.
	_ = s.blob.Delete(ctx, bucket.LegacyDataKey(account))
	return nil
}

// DeleteAccount borra los datos guardados de una cuenta (no sus archivos).
func (s *Store) DeleteAccount(ctx context.Context, account string) error {
	s.forget(account)
	_ = s.blob.Delete(ctx, bucket.LegacyDataKey(account))
	return s.blob.Delete(ctx, bucket.DataKey(account))
}

// Known devuelve cuáles de estos correos ya fueron revisados.
func (s *Store) Known(ctx context.Context, account string, ids []string) (map[string]bool, error) {
	doc, err := s.view(ctx, account)
	if err != nil {
		return nil, err
	}
	wanted := make(map[string]bool, len(ids))
	for _, id := range ids {
		wanted[id] = true
	}
	known := map[string]bool{}
	for _, id := range doc.Scanned {
		if wanted[id] {
			known[id] = true
		}
	}
	return known, nil
}

// SaveSync guarda el resultado de revisar una tanda de correos: las facturas
// encontradas y los correos que ya no hay que volver a revisar. Si una
// factura ya existía (el mismo DTE llegó en otro correo) se actualizan los
// datos del documento pero se conservan el estado, la categoría y las notas
// del usuario. Devuelve cuántas facturas son nuevas.
func (s *Store) SaveSync(ctx context.Context, account string, found []Invoice, scanned []string) (int, error) {
	imported := 0
	err := s.update(ctx, account, func(doc *document) error {
		byCode := make(map[string]int, len(doc.Invoices))
		for i, old := range doc.Invoices {
			byCode[old.CodigoGeneracion] = i
		}
		for _, invoice := range found {
			if i, exists := byCode[invoice.CodigoGeneracion]; exists {
				old := doc.Invoices[i]
				invoice.ID, invoice.Status, invoice.Category, invoice.Notes = old.ID, old.Status, old.Category, old.Notes
				invoice.MessageID, invoice.MailFrom, invoice.MailSubject = old.MessageID, old.MailFrom, old.MailSubject
				doc.Invoices[i] = invoice
				continue
			}
			invoice.ID = doc.NextInvoiceID
			doc.NextInvoiceID++
			invoice.Status = "pendiente"
			byCode[invoice.CodigoGeneracion] = len(doc.Invoices)
			doc.Invoices = append(doc.Invoices, invoice)
			imported++
		}

		seen := make(map[string]bool, len(doc.Scanned))
		for _, id := range doc.Scanned {
			seen[id] = true
		}
		for _, id := range scanned {
			if !seen[id] {
				seen[id] = true
				doc.Scanned = append(doc.Scanned, id)
			}
		}
		return nil
	})
	return imported, err
}

// Filter son los criterios para listar facturas. Los campos vacíos no filtran.
type Filter struct {
	From   string // fecha mínima, AAAA-MM-DD
	To     string // fecha máxima, AAAA-MM-DD
	Status string
	Tipo   string
	Search string // emisor, NIT, NRC, número de control o categoría
	// Sort es el orden: "" (fecha, lo más nuevo primero), "fecha_asc",
	// "total_desc", "total_asc" o "emisor_asc".
	Sort   string
	Limit  int // 0 = sin límite
	Offset int
}

// Sorts son los órdenes que acepta Filter.Sort.
var Sorts = []string{"", "fecha_asc", "total_desc", "total_asc", "emisor_asc"}

func (f Filter) matches(i Invoice) bool {
	if f.From != "" && i.Fecha < f.From {
		return false
	}
	if f.To != "" && i.Fecha > f.To {
		return false
	}
	if f.Status != "" && i.Status != f.Status {
		return false
	}
	if f.Tipo != "" && i.TipoDte != f.Tipo {
		return false
	}
	if f.Search == "" {
		return true
	}
	q := strings.ToLower(f.Search)
	for _, field := range []string{i.EmisorNombre, i.EmisorNIT, i.EmisorNRC, i.NumeroControl, i.Category} {
		if strings.Contains(strings.ToLower(field), q) {
			return true
		}
	}
	return false
}

// Totals son las sumas de un conjunto de facturas (las notas de crédito restan).
type Totals struct {
	Count   int     `json:"count"`
	Gravado float64 `json:"gravado"`
	IVA     float64 `json:"iva"`
	Total   float64 `json:"total"`
}

func (t *Totals) add(i Invoice) {
	sign := 1.0
	if i.TipoDte == "05" { // nota de crédito
		sign = -1
	}
	t.Count++
	t.Gravado += sign * i.Gravado
	t.IVA += sign * i.IVA
	t.Total += sign * i.Total
}

// round quita los decimales de más que deja sumar números con punto flotante.
func (t *Totals) round() {
	cents := func(v float64) float64 {
		if v < 0 {
			return float64(int64(v*100-0.5)) / 100
		}
		return float64(int64(v*100+0.5)) / 100
	}
	t.Gravado, t.IVA, t.Total = cents(t.Gravado), cents(t.IVA), cents(t.Total)
}

// List devuelve las facturas que cumplen el filtro, de la más reciente a la
// más antigua, y las sumas de todas las que cumplen (no solo de la página).
func (s *Store) List(ctx context.Context, account string, f Filter) ([]Invoice, Totals, error) {
	var totals Totals
	doc, err := s.view(ctx, account)
	if err != nil {
		return nil, totals, err
	}

	items := []Invoice{}
	for _, invoice := range doc.Invoices {
		if f.matches(invoice) {
			items = append(items, invoice)
			totals.add(invoice)
		}
	}
	totals.round()

	newest := func(a, b Invoice) int {
		return cmp.Or(cmp.Compare(b.Fecha, a.Fecha), cmp.Compare(b.Hora, a.Hora), cmp.Compare(b.ID, a.ID))
	}
	slices.SortFunc(items, func(a, b Invoice) int {
		switch f.Sort {
		case "fecha_asc":
			return -newest(a, b)
		case "total_desc":
			return cmp.Or(cmp.Compare(b.Total, a.Total), newest(a, b))
		case "total_asc":
			return cmp.Or(cmp.Compare(a.Total, b.Total), newest(a, b))
		case "emisor_asc":
			return cmp.Or(cmp.Compare(strings.ToLower(a.EmisorNombre), strings.ToLower(b.EmisorNombre)), newest(a, b))
		}
		return newest(a, b)
	})

	if f.Limit > 0 {
		start := min(f.Offset, len(items))
		items = items[start:min(start+f.Limit, len(items))]
	}
	return items, totals, nil
}

// StatusCounts cuenta, para cada estado, las facturas que cumplen el filtro
// sin tener en cuenta el estado elegido. Sirve para las pestañas
// "Pendientes (12) · Revisadas (3) · Pagadas (40)".
func (s *Store) StatusCounts(ctx context.Context, account string, f Filter) (map[string]int, error) {
	doc, err := s.view(ctx, account)
	if err != nil {
		return nil, err
	}
	f.Status = ""
	counts := map[string]int{}
	for _, invoice := range doc.Invoices {
		if f.matches(invoice) {
			counts[invoice.Status]++
		}
	}
	return counts, nil
}

// UpdateMany cambia el estado de varias facturas de una vez. Devuelve
// cuántas existían y quedaron con ese estado.
func (s *Store) UpdateMany(ctx context.Context, account string, ids []int64, status string) (int, error) {
	updated := 0
	err := s.update(ctx, account, func(doc *document) error {
		wanted := make(map[int64]bool, len(ids))
		for _, id := range ids {
			wanted[id] = true
		}
		for i := range doc.Invoices {
			if wanted[doc.Invoices[i].ID] {
				doc.Invoices[i].Status = status
				updated++
			}
		}
		return nil
	})
	return updated, err
}

// ErrNotFound indica que la factura o el archivo no existen en esta cuenta.
var ErrNotFound = errors.New("not found")

// Patch son los campos que el usuario puede cambiar; nil = no tocar.
type Patch struct {
	Status   *string `json:"status"`
	Category *string `json:"category"`
	Notes    *string `json:"notes"`
}

func (s *Store) Update(ctx context.Context, account string, id int64, p Patch) (Invoice, error) {
	var updated Invoice
	err := s.update(ctx, account, func(doc *document) error {
		i := slices.IndexFunc(doc.Invoices, func(invoice Invoice) bool { return invoice.ID == id })
		if i < 0 {
			return ErrNotFound
		}
		invoice := &doc.Invoices[i]
		if p.Status != nil {
			invoice.Status = *p.Status
		}
		if p.Category != nil {
			invoice.Category = *p.Category
		}
		if p.Notes != nil {
			invoice.Notes = *p.Notes
		}
		updated = *invoice
		return nil
	})
	return updated, err
}

type MonthTotal struct {
	Month string `json:"month"` // AAAA-MM
	Totals
}

type IssuerTotal struct {
	Nombre string `json:"nombre"`
	NIT    string `json:"nit"`
	Totals
}

// NamedTotal son las sumas de un grupo con nombre (un tipo de documento,
// una categoría).
type NamedTotal struct {
	Name string `json:"name"`
	Totals
}

type Summary struct {
	Months     []MonthTotal   `json:"months"`
	Issuers    []IssuerTotal  `json:"issuers"`
	ByStatus   map[string]int `json:"byStatus"`
	Categories []string       `json:"categories"`
	// Recent son las últimas facturas, de la más nueva a la más vieja.
	Recent []Invoice `json:"recent"`
	// Types son las sumas por tipo de documento (01, 03, ...).
	Types []NamedTotal `json:"types"`
	// CategoryTotals son las sumas por categoría; las facturas sin
	// categoría van bajo el nombre "".
	CategoryTotals []NamedTotal `json:"categoryTotals"`
	// Pending es lo que suman las facturas que no están pagadas.
	Pending Totals `json:"pending"`
}

// Summary resume las facturas con fecha desde `since` (AAAA-MM-DD): totales
// por mes, los seis emisores a los que más se les compra, cuántas facturas
// hay en cada estado y las categorías que el usuario ha usado.
func (s *Store) Summary(ctx context.Context, account, since string) (Summary, error) {
	out := Summary{
		Months:         []MonthTotal{},
		Issuers:        []IssuerTotal{},
		ByStatus:       map[string]int{},
		Categories:     []string{},
		Recent:         []Invoice{},
		Types:          []NamedTotal{},
		CategoryTotals: []NamedTotal{},
	}
	doc, err := s.view(ctx, account)
	if err != nil {
		return out, err
	}

	months := map[string]*MonthTotal{}
	issuers := map[string]*IssuerTotal{}
	types := map[string]*NamedTotal{}
	categories := map[string]*NamedTotal{}
	named := func(group map[string]*NamedTotal, name string, invoice Invoice) {
		if group[name] == nil {
			group[name] = &NamedTotal{Name: name}
		}
		group[name].add(invoice)
	}

	for _, invoice := range doc.Invoices {
		out.ByStatus[invoice.Status]++
		if invoice.Status != "pagada" {
			out.Pending.add(invoice)
		}
		if invoice.Category != "" && !slices.Contains(out.Categories, invoice.Category) {
			out.Categories = append(out.Categories, invoice.Category)
		}
		if invoice.Fecha < since || len(invoice.Fecha) < 7 {
			continue
		}

		month := invoice.Fecha[:7]
		if months[month] == nil {
			months[month] = &MonthTotal{Month: month}
		}
		months[month].add(invoice)

		if issuers[invoice.EmisorNIT] == nil {
			issuers[invoice.EmisorNIT] = &IssuerTotal{Nombre: invoice.EmisorNombre, NIT: invoice.EmisorNIT}
		}
		issuers[invoice.EmisorNIT].add(invoice)

		named(types, invoice.TipoDte, invoice)
		named(categories, invoice.Category, invoice)
	}
	out.Pending.round()

	for _, group := range []struct {
		from map[string]*NamedTotal
		to   *[]NamedTotal
	}{{types, &out.Types}, {categories, &out.CategoryTotals}} {
		for _, total := range group.from {
			total.round()
			*group.to = append(*group.to, *total)
		}
		slices.SortFunc(*group.to, func(a, b NamedTotal) int {
			return cmp.Or(cmp.Compare(b.Total, a.Total), cmp.Compare(a.Name, b.Name))
		})
	}

	// Las últimas facturas: se ordena una copia, no la lista compartida.
	recent := slices.Clone(doc.Invoices)
	slices.SortFunc(recent, func(a, b Invoice) int {
		return cmp.Or(cmp.Compare(b.Fecha, a.Fecha), cmp.Compare(b.Hora, a.Hora), cmp.Compare(b.ID, a.ID))
	})
	out.Recent = slices.Clone(recent[:min(6, len(recent))])

	for _, m := range months {
		m.round()
		out.Months = append(out.Months, *m)
	}
	slices.SortFunc(out.Months, func(a, b MonthTotal) int { return cmp.Compare(a.Month, b.Month) })

	for _, issuer := range issuers {
		issuer.round()
		out.Issuers = append(out.Issuers, *issuer)
	}
	slices.SortFunc(out.Issuers, func(a, b IssuerTotal) int {
		return cmp.Or(cmp.Compare(b.Total, a.Total), cmp.Compare(a.Nombre, b.Nombre))
	})
	out.Issuers = out.Issuers[:min(6, len(out.Issuers))]

	slices.Sort(out.Categories)
	return out, nil
}

// InvoiceOfMessage devuelve la fecha y el emisor de la factura que llegó en
// un correo. ok es false si ese correo no trajo ninguna factura conocida.
func (s *Store) InvoiceOfMessage(ctx context.Context, account, messageID string) (fecha, emisor string, ok bool) {
	doc, err := s.view(ctx, account)
	if err != nil {
		return "", "", false
	}
	for _, invoice := range doc.Invoices {
		if invoice.MessageID == messageID {
			return invoice.Fecha, invoice.EmisorNombre, true
		}
	}
	return "", "", false
}
