// Package store guarda en Postgres las facturas (DTE) leídas del correo,
// junto con lo que el usuario anota sobre ellas: estado, categoría y notas.
package store

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const schema = `
CREATE TABLE IF NOT EXISTS invoices (
	id                BIGSERIAL PRIMARY KEY,
	owner             TEXT NOT NULL,
	codigo_generacion TEXT NOT NULL,
	numero_control    TEXT NOT NULL DEFAULT '',
	tipo_dte          TEXT NOT NULL DEFAULT '',
	ambiente          TEXT NOT NULL DEFAULT '',
	fecha             TEXT NOT NULL DEFAULT '',
	hora              TEXT NOT NULL DEFAULT '',
	moneda            TEXT NOT NULL DEFAULT 'USD',
	emisor_nit        TEXT NOT NULL DEFAULT '',
	emisor_nrc        TEXT NOT NULL DEFAULT '',
	emisor_nombre     TEXT NOT NULL DEFAULT '',
	receptor_doc      TEXT NOT NULL DEFAULT '',
	receptor_nrc      TEXT NOT NULL DEFAULT '',
	receptor_nombre   TEXT NOT NULL DEFAULT '',
	no_sujeto         NUMERIC(14,2) NOT NULL DEFAULT 0,
	exento            NUMERIC(14,2) NOT NULL DEFAULT 0,
	gravado           NUMERIC(14,2) NOT NULL DEFAULT 0,
	iva               NUMERIC(14,2) NOT NULL DEFAULT 0,
	iva_percibido     NUMERIC(14,2) NOT NULL DEFAULT 0,
	iva_retenido      NUMERIC(14,2) NOT NULL DEFAULT 0,
	total             NUMERIC(14,2) NOT NULL DEFAULT 0,
	sello             TEXT NOT NULL DEFAULT '',
	message_id        TEXT NOT NULL DEFAULT '',
	mail_from         TEXT NOT NULL DEFAULT '',
	mail_subject      TEXT NOT NULL DEFAULT '',
	status            TEXT NOT NULL DEFAULT 'pendiente',
	category          TEXT NOT NULL DEFAULT '',
	notes             TEXT NOT NULL DEFAULT '',
	created_at        TIMESTAMPTZ NOT NULL DEFAULT now(),
	updated_at        TIMESTAMPTZ NOT NULL DEFAULT now(),
	UNIQUE (owner, codigo_generacion)
);
CREATE INDEX IF NOT EXISTS invoices_owner_fecha ON invoices (owner, fecha DESC);

-- Correos ya revisados, traigan o no un DTE, para no volver a descargarlos.
CREATE TABLE IF NOT EXISTS scanned_messages (
	owner      TEXT NOT NULL,
	message_id TEXT NOT NULL,
	PRIMARY KEY (owner, message_id)
);
`

// Statuses son los estados que el usuario puede asignar a una factura.
var Statuses = []string{"pendiente", "revisada", "pagada"}

// sign hace que las notas de crédito (tipo 05) resten en las sumas.
const sign = "(CASE WHEN tipo_dte = '05' THEN -1 ELSE 1 END)"

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

const columns = `id, codigo_generacion, numero_control, tipo_dte, ambiente, fecha, hora, moneda,
	emisor_nit, emisor_nrc, emisor_nombre, receptor_doc, receptor_nrc, receptor_nombre,
	no_sujeto::float8, exento::float8, gravado::float8, iva::float8,
	iva_percibido::float8, iva_retenido::float8, total::float8,
	sello, message_id, mail_from, mail_subject, status, category, notes`

func scan(row pgx.Row) (Invoice, error) {
	var i Invoice
	err := row.Scan(&i.ID, &i.CodigoGeneracion, &i.NumeroControl, &i.TipoDte, &i.Ambiente,
		&i.Fecha, &i.Hora, &i.Moneda, &i.EmisorNIT, &i.EmisorNRC, &i.EmisorNombre,
		&i.ReceptorDoc, &i.ReceptorNRC, &i.ReceptorNombre,
		&i.NoSujeto, &i.Exento, &i.Gravado, &i.IVA, &i.IVAPercibido, &i.IVARetenido, &i.Total,
		&i.Sello, &i.MessageID, &i.MailFrom, &i.MailSubject, &i.Status, &i.Category, &i.Notes)
	return i, err
}

type Store struct {
	pool *pgxpool.Pool
}

// Open se conecta a Postgres y crea las tablas si no existen. Reintenta
// unos segundos porque al levantar con Docker la base puede tardar en estar lista.
func Open(ctx context.Context, dsn string) (*Store, error) {
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		return nil, err
	}

	for attempt := 1; ; attempt++ {
		if _, err = pool.Exec(ctx, schema+filesSchema); err == nil {
			return &Store{pool: pool}, nil
		}
		if attempt == 15 {
			pool.Close()
			return nil, fmt.Errorf("database not ready: %w", err)
		}
		select {
		case <-ctx.Done():
			pool.Close()
			return nil, ctx.Err()
		case <-time.After(2 * time.Second):
		}
	}
}

func (s *Store) Close() { s.pool.Close() }

// DeleteOwner borra todo lo guardado de una cuenta.
func (s *Store) DeleteOwner(ctx context.Context, owner string) error {
	if _, err := s.pool.Exec(ctx, `DELETE FROM invoices WHERE owner = $1`, owner); err != nil {
		return err
	}
	if _, err := s.pool.Exec(ctx, `DELETE FROM saved_files WHERE owner = $1`, owner); err != nil {
		return err
	}
	_, err := s.pool.Exec(ctx, `DELETE FROM scanned_messages WHERE owner = $1`, owner)
	return err
}

// Known devuelve cuáles de estos correos ya fueron revisados.
func (s *Store) Known(ctx context.Context, owner string, ids []string) (map[string]bool, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT message_id FROM scanned_messages WHERE owner = $1 AND message_id = ANY($2)`, owner, ids)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	known := make(map[string]bool, len(ids))
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		known[id] = true
	}
	return known, rows.Err()
}

// MarkScanned registra que un correo ya fue revisado.
func (s *Store) MarkScanned(ctx context.Context, owner, messageID string) error {
	_, err := s.pool.Exec(ctx,
		`INSERT INTO scanned_messages (owner, message_id) VALUES ($1, $2) ON CONFLICT DO NOTHING`,
		owner, messageID)
	return err
}

// Upsert guarda una factura. Si ya existía (el mismo DTE llegó en otro
// correo) actualiza los datos del documento pero conserva el estado, la
// categoría y las notas del usuario. Devuelve true si la factura es nueva.
func (s *Store) Upsert(ctx context.Context, owner string, i Invoice) (bool, error) {
	var inserted bool
	err := s.pool.QueryRow(ctx, `
		INSERT INTO invoices (owner, codigo_generacion, numero_control, tipo_dte, ambiente, fecha, hora,
			moneda, emisor_nit, emisor_nrc, emisor_nombre, receptor_doc, receptor_nrc, receptor_nombre,
			no_sujeto, exento, gravado, iva, iva_percibido, iva_retenido, total, sello,
			message_id, mail_from, mail_subject)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,$20,$21,$22,$23,$24,$25)
		ON CONFLICT (owner, codigo_generacion) DO UPDATE SET
			numero_control = EXCLUDED.numero_control, tipo_dte = EXCLUDED.tipo_dte,
			ambiente = EXCLUDED.ambiente, fecha = EXCLUDED.fecha, hora = EXCLUDED.hora,
			moneda = EXCLUDED.moneda, emisor_nit = EXCLUDED.emisor_nit, emisor_nrc = EXCLUDED.emisor_nrc,
			emisor_nombre = EXCLUDED.emisor_nombre, receptor_doc = EXCLUDED.receptor_doc,
			receptor_nrc = EXCLUDED.receptor_nrc, receptor_nombre = EXCLUDED.receptor_nombre,
			no_sujeto = EXCLUDED.no_sujeto, exento = EXCLUDED.exento, gravado = EXCLUDED.gravado,
			iva = EXCLUDED.iva, iva_percibido = EXCLUDED.iva_percibido,
			iva_retenido = EXCLUDED.iva_retenido, total = EXCLUDED.total, sello = EXCLUDED.sello,
			updated_at = now()
		RETURNING (xmax = 0)`,
		owner, i.CodigoGeneracion, i.NumeroControl, i.TipoDte, i.Ambiente, i.Fecha, i.Hora,
		i.Moneda, i.EmisorNIT, i.EmisorNRC, i.EmisorNombre, i.ReceptorDoc, i.ReceptorNRC, i.ReceptorNombre,
		i.NoSujeto, i.Exento, i.Gravado, i.IVA, i.IVAPercibido, i.IVARetenido, i.Total, i.Sello,
		i.MessageID, i.MailFrom, i.MailSubject).Scan(&inserted)
	return inserted, err
}

// Filter son los criterios para listar facturas. Los campos vacíos no filtran.
type Filter struct {
	From   string // fecha mínima, AAAA-MM-DD
	To     string // fecha máxima, AAAA-MM-DD
	Status string
	Tipo   string
	Search string // emisor, NIT, NRC, número de control o categoría
	Limit  int    // 0 = sin límite
	Offset int
}

func (f Filter) where(owner string) (string, []any) {
	clauses := []string{"owner = $1"}
	args := []any{owner}
	add := func(clause string, value any) {
		args = append(args, value)
		clauses = append(clauses, fmt.Sprintf(clause, len(args)))
	}

	if f.From != "" {
		add("fecha >= $%d", f.From)
	}
	if f.To != "" {
		add("fecha <= $%d", f.To)
	}
	if f.Status != "" {
		add("status = $%d", f.Status)
	}
	if f.Tipo != "" {
		add("tipo_dte = $%d", f.Tipo)
	}
	if f.Search != "" {
		args = append(args, "%"+strings.ToLower(f.Search)+"%")
		n := len(args)
		clauses = append(clauses, fmt.Sprintf(
			"(lower(emisor_nombre) LIKE $%d OR emisor_nit LIKE $%d OR emisor_nrc LIKE $%d OR lower(numero_control) LIKE $%d OR lower(category) LIKE $%d)",
			n, n, n, n, n))
	}
	return strings.Join(clauses, " AND "), args
}

// Totals son las sumas de un conjunto de facturas (las notas de crédito restan).
type Totals struct {
	Count   int     `json:"count"`
	Gravado float64 `json:"gravado"`
	IVA     float64 `json:"iva"`
	Total   float64 `json:"total"`
}

const totals = `count(*)::int, coalesce(sum(gravado * ` + sign + `), 0)::float8,
	coalesce(sum(iva * ` + sign + `), 0)::float8, coalesce(sum(total * ` + sign + `), 0)::float8`

// List devuelve las facturas que cumplen el filtro, de la más reciente a la
// más antigua, y las sumas de todas las que cumplen (no solo de la página).
func (s *Store) List(ctx context.Context, owner string, f Filter) ([]Invoice, Totals, error) {
	where, args := f.where(owner)

	var t Totals
	err := s.pool.QueryRow(ctx, "SELECT "+totals+" FROM invoices WHERE "+where, args...).
		Scan(&t.Count, &t.Gravado, &t.IVA, &t.Total)
	if err != nil {
		return nil, t, err
	}

	query := "SELECT " + columns + " FROM invoices WHERE " + where + " ORDER BY fecha DESC, hora DESC, id DESC"
	if f.Limit > 0 {
		query += fmt.Sprintf(" LIMIT %d OFFSET %d", f.Limit, f.Offset)
	}
	rows, err := s.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, t, err
	}
	defer rows.Close()

	items := []Invoice{}
	for rows.Next() {
		i, err := scan(rows)
		if err != nil {
			return nil, t, err
		}
		items = append(items, i)
	}
	return items, t, rows.Err()
}

// ErrNotFound indica que la factura no existe o es de otra cuenta.
var ErrNotFound = errors.New("invoice not found")

// Patch son los campos que el usuario puede cambiar; nil = no tocar.
type Patch struct {
	Status   *string `json:"status"`
	Category *string `json:"category"`
	Notes    *string `json:"notes"`
}

func (s *Store) Update(ctx context.Context, owner string, id int64, p Patch) (Invoice, error) {
	row := s.pool.QueryRow(ctx, `
		UPDATE invoices SET
			status = coalesce($3, status),
			category = coalesce($4, category),
			notes = coalesce($5, notes),
			updated_at = now()
		WHERE owner = $1 AND id = $2
		RETURNING `+columns, owner, id, p.Status, p.Category, p.Notes)

	i, err := scan(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return i, ErrNotFound
	}
	return i, err
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

type Summary struct {
	Months     []MonthTotal   `json:"months"`
	Issuers    []IssuerTotal  `json:"issuers"`
	ByStatus   map[string]int `json:"byStatus"`
	Categories []string       `json:"categories"`
}

// Summary resume las facturas con fecha desde `since` (AAAA-MM-DD): totales
// por mes, emisores a los que más se les compra y cuántas hay en cada estado.
func (s *Store) Summary(ctx context.Context, owner, since string) (Summary, error) {
	out := Summary{
		Months:     []MonthTotal{},
		Issuers:    []IssuerTotal{},
		ByStatus:   map[string]int{},
		Categories: []string{},
	}

	rows, err := s.pool.Query(ctx, `
		SELECT substr(fecha, 1, 7), `+totals+`
		FROM invoices WHERE owner = $1 AND fecha >= $2
		GROUP BY 1 ORDER BY 1`, owner, since)
	if err != nil {
		return out, err
	}
	for rows.Next() {
		var m MonthTotal
		if err := rows.Scan(&m.Month, &m.Count, &m.Gravado, &m.IVA, &m.Total); err != nil {
			rows.Close()
			return out, err
		}
		out.Months = append(out.Months, m)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return out, err
	}

	rows, err = s.pool.Query(ctx, `
		SELECT max(emisor_nombre), emisor_nit, `+totals+`
		FROM invoices WHERE owner = $1 AND fecha >= $2
		GROUP BY emisor_nit ORDER BY 6 DESC LIMIT 6`, owner, since)
	if err != nil {
		return out, err
	}
	for rows.Next() {
		var e IssuerTotal
		if err := rows.Scan(&e.Nombre, &e.NIT, &e.Count, &e.Gravado, &e.IVA, &e.Total); err != nil {
			rows.Close()
			return out, err
		}
		out.Issuers = append(out.Issuers, e)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return out, err
	}

	rows, err = s.pool.Query(ctx,
		`SELECT status, count(*)::int FROM invoices WHERE owner = $1 GROUP BY status`, owner)
	if err != nil {
		return out, err
	}
	for rows.Next() {
		var status string
		var n int
		if err := rows.Scan(&status, &n); err != nil {
			rows.Close()
			return out, err
		}
		out.ByStatus[status] = n
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return out, err
	}

	rows, err = s.pool.Query(ctx,
		`SELECT DISTINCT category FROM invoices WHERE owner = $1 AND category <> '' ORDER BY 1`, owner)
	if err != nil {
		return out, err
	}
	defer rows.Close()
	for rows.Next() {
		var category string
		if err := rows.Scan(&category); err != nil {
			return out, err
		}
		out.Categories = append(out.Categories, category)
	}
	return out, rows.Err()
}
