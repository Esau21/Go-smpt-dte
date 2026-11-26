package store

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
)

// filesSchema crea la tabla de archivos guardados "en la nube": los
// adjuntos (PDF, JSON) que el usuario decide conservar.
//
// El contenido vive en el bucket, en la ruta `object_key`; aquí solo queda
// el registro. La columna `content` se conserva para los archivos guardados
// antes de que existiera el bucket, y para cuando no hay bucket configurado.
const filesSchema = `
CREATE TABLE IF NOT EXISTS saved_files (
	id           BIGSERIAL PRIMARY KEY,
	owner        TEXT NOT NULL,
	message_id   TEXT NOT NULL,
	filename     TEXT NOT NULL,
	size         INT NOT NULL,
	content      BYTEA,
	mail_from    TEXT NOT NULL DEFAULT '',
	mail_subject TEXT NOT NULL DEFAULT '',
	created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
	UNIQUE (owner, message_id, filename)
);
CREATE INDEX IF NOT EXISTS saved_files_owner ON saved_files (owner, created_at DESC);
ALTER TABLE saved_files ADD COLUMN IF NOT EXISTS object_key TEXT NOT NULL DEFAULT '';
ALTER TABLE saved_files ALTER COLUMN content DROP NOT NULL;
`

// SavedFile describe un archivo guardado, sin su contenido.
type SavedFile struct {
	ID          int64     `json:"id"`
	MessageID   string    `json:"messageId"`
	Filename    string    `json:"filename"`
	Size        int       `json:"size"`
	MailFrom    string    `json:"mailFrom"`
	MailSubject string    `json:"mailSubject"`
	CreatedAt   time.Time `json:"createdAt"`
	// Path es la ruta del archivo dentro del bucket; vacía si el archivo
	// está guardado en la base de datos.
	Path string `json:"path"`
}

const fileColumns = `id, message_id, filename, size, mail_from, mail_subject, created_at, object_key`

func scanFile(row pgx.Row) (SavedFile, error) {
	var f SavedFile
	err := row.Scan(&f.ID, &f.MessageID, &f.Filename, &f.Size, &f.MailFrom, &f.MailSubject, &f.CreatedAt, &f.Path)
	return f, err
}

// SaveFile registra un archivo guardado. Si `f.Path` trae la ruta del
// bucket, el contenido no se guarda aquí (solo su tamaño); si viene vacía,
// el contenido queda en la base de datos. Guardar dos veces el mismo adjunto
// reemplaza el registro en vez de duplicarlo.
func (s *Store) SaveFile(ctx context.Context, owner string, f SavedFile, content []byte) (SavedFile, error) {
	var stored []byte
	if f.Path == "" {
		stored = content
	}
	row := s.pool.QueryRow(ctx, `
		INSERT INTO saved_files (owner, message_id, filename, size, content, mail_from, mail_subject, object_key)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
		ON CONFLICT (owner, message_id, filename) DO UPDATE SET
			size = EXCLUDED.size, content = EXCLUDED.content, object_key = EXCLUDED.object_key,
			mail_from = EXCLUDED.mail_from, mail_subject = EXCLUDED.mail_subject
		RETURNING `+fileColumns,
		owner, f.MessageID, f.Filename, len(content), stored, f.MailFrom, f.MailSubject, f.Path)
	return scanFile(row)
}

// ListFiles devuelve los archivos guardados, ordenados por su ruta (año,
// mes y emisor) con lo más reciente primero. Si messageID no está vacío,
// solo los de ese correo.
func (s *Store) ListFiles(ctx context.Context, owner, messageID string) ([]SavedFile, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT `+fileColumns+` FROM saved_files
		WHERE owner = $1 AND ($2 = '' OR message_id = $2)
		ORDER BY object_key DESC, created_at DESC, id DESC`, owner, messageID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	files := []SavedFile{}
	for rows.Next() {
		f, err := scanFile(rows)
		if err != nil {
			return nil, err
		}
		files = append(files, f)
	}
	return files, rows.Err()
}

// FileContent devuelve un archivo guardado. `content` es nil cuando el
// contenido está en el bucket (en `Path`).
func (s *Store) FileContent(ctx context.Context, owner string, id int64) (SavedFile, []byte, error) {
	var content []byte
	var f SavedFile
	err := s.pool.QueryRow(ctx, `
		SELECT `+fileColumns+`, content FROM saved_files WHERE owner = $1 AND id = $2`, owner, id).
		Scan(&f.ID, &f.MessageID, &f.Filename, &f.Size, &f.MailFrom, &f.MailSubject, &f.CreatedAt, &f.Path, &content)
	if errors.Is(err, pgx.ErrNoRows) {
		return f, nil, ErrNotFound
	}
	return f, content, err
}

// DeleteFile borra el registro y devuelve la ruta que tenía en el bucket
// (vacía si estaba en la base de datos), para borrar también el objeto.
func (s *Store) DeleteFile(ctx context.Context, owner string, id int64) (string, error) {
	var path string
	err := s.pool.QueryRow(ctx,
		`DELETE FROM saved_files WHERE owner = $1 AND id = $2 RETURNING object_key`, owner, id).Scan(&path)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", ErrNotFound
	}
	return path, err
}

// KeyTaken dice si otra entrada de esta cuenta (de un correo distinto) ya
// usa esa ruta del bucket.
func (s *Store) KeyTaken(ctx context.Context, owner, key, messageID string) (bool, error) {
	var taken bool
	err := s.pool.QueryRow(ctx, `
		SELECT EXISTS (SELECT 1 FROM saved_files WHERE owner = $1 AND object_key = $2 AND message_id <> $3)`,
		owner, key, messageID).Scan(&taken)
	return taken, err
}

// LegacyFiles devuelve los archivos de la cuenta cuyo contenido sigue en la
// base de datos, para poder pasarlos al bucket.
func (s *Store) LegacyFiles(ctx context.Context, owner string) ([]int64, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT id FROM saved_files WHERE owner = $1 AND object_key = '' AND content IS NOT NULL`, owner)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// MoveToBucket anota que el contenido de un archivo ya está en el bucket y
// lo quita de la base de datos.
func (s *Store) MoveToBucket(ctx context.Context, owner string, id int64, key string) error {
	_, err := s.pool.Exec(ctx,
		`UPDATE saved_files SET object_key = $3, content = NULL WHERE owner = $1 AND id = $2`, owner, id, key)
	return err
}

// InvoiceOfMessage devuelve la fecha y el emisor de la factura que llegó en
// un correo. ok es false si ese correo no trajo ninguna factura conocida.
func (s *Store) InvoiceOfMessage(ctx context.Context, owner, messageID string) (fecha, emisor string, ok bool) {
	err := s.pool.QueryRow(ctx, `
		SELECT fecha, emisor_nombre FROM invoices
		WHERE owner = $1 AND message_id = $2 ORDER BY id LIMIT 1`, owner, messageID).Scan(&fecha, &emisor)
	return fecha, emisor, err == nil
}
