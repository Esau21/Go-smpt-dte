package store

import (
	"cmp"
	"context"
	"errors"
	"slices"
	"strings"
	"time"
)

// SaveFile registra un archivo que ya se subió al bucket (en `f.Path`).
// Guardar dos veces el mismo adjunto de un correo reemplaza el registro en
// vez de duplicarlo.
func (s *Store) SaveFile(ctx context.Context, account string, f SavedFile) (SavedFile, error) {
	err := s.update(ctx, account, func(doc *document) error {
		f.CreatedAt = time.Now().UTC()
		i := slices.IndexFunc(doc.Files, func(old SavedFile) bool {
			return old.MessageID == f.MessageID && old.Filename == f.Filename
		})
		if i >= 0 {
			f.ID = doc.Files[i].ID
			doc.Files[i] = f
			return nil
		}
		f.ID = doc.NextFileID
		doc.NextFileID++
		doc.Files = append(doc.Files, f)
		return nil
	})
	return f, err
}

// ListFiles devuelve los archivos guardados, ordenados por su ruta (año,
// mes y emisor) con lo más reciente primero. Si messageID no está vacío,
// solo los de ese correo.
func (s *Store) ListFiles(ctx context.Context, account, messageID string) ([]SavedFile, error) {
	doc, err := s.view(ctx, account)
	if err != nil {
		return nil, err
	}
	files := []SavedFile{}
	for _, f := range doc.Files {
		if messageID == "" || f.MessageID == messageID {
			files = append(files, f)
		}
	}
	slices.SortFunc(files, func(a, b SavedFile) int {
		return cmp.Or(cmp.Compare(b.Path, a.Path), b.CreatedAt.Compare(a.CreatedAt))
	})
	return files, nil
}

// File devuelve el registro de un archivo guardado.
func (s *Store) File(ctx context.Context, account string, id int64) (SavedFile, error) {
	doc, err := s.view(ctx, account)
	if err != nil {
		return SavedFile{}, err
	}
	for _, f := range doc.Files {
		if f.ID == id {
			return f, nil
		}
	}
	return SavedFile{}, ErrNotFound
}

// DeleteFile borra el registro de un archivo y devuelve la ruta que tenía
// en el bucket, para borrar también el objeto.
func (s *Store) DeleteFile(ctx context.Context, account string, id int64) (string, error) {
	var path string
	err := s.update(ctx, account, func(doc *document) error {
		i := slices.IndexFunc(doc.Files, func(f SavedFile) bool { return f.ID == id })
		if i < 0 {
			return ErrNotFound
		}
		path = doc.Files[i].Path
		doc.Files = slices.Delete(doc.Files, i, i+1)
		return nil
	})
	return path, err
}

// KeyTaken dice si un archivo de otro correo ya ocupa esa ruta del bucket.
func (s *Store) KeyTaken(ctx context.Context, account, key, messageID string) (bool, error) {
	doc, err := s.view(ctx, account)
	if err != nil {
		return false, err
	}
	return slices.ContainsFunc(doc.Files, func(f SavedFile) bool {
		return f.Path == key && f.MessageID != messageID
	}), nil
}

// Folders devuelve las carpetas que el usuario creó a mano.
func (s *Store) Folders(ctx context.Context, account string) ([]string, error) {
	doc, err := s.view(ctx, account)
	if err != nil {
		return nil, err
	}
	folders := slices.Clone(doc.Folders)
	if folders == nil {
		folders = []string{}
	}
	slices.Sort(folders)
	return folders, nil
}

// AddFolder registra una carpeta creada por el usuario.
func (s *Store) AddFolder(ctx context.Context, account, folder string) error {
	return s.update(ctx, account, func(doc *document) error {
		if !slices.Contains(doc.Folders, folder) {
			doc.Folders = append(doc.Folders, folder)
		}
		return nil
	})
}

// ErrNotEmpty indica que la carpeta todavía tiene archivos o carpetas dentro.
var ErrNotEmpty = errors.New("folder not empty")

// RemoveFolder borra una carpeta creada por el usuario, solo si está vacía.
// `prefix` es la ruta completa de la carpeta en el bucket (cuenta/carpeta).
func (s *Store) RemoveFolder(ctx context.Context, account, folder, prefix string) error {
	return s.update(ctx, account, func(doc *document) error {
		if slices.ContainsFunc(doc.Files, func(f SavedFile) bool { return strings.HasPrefix(f.Path, prefix+"/") }) ||
			slices.ContainsFunc(doc.Folders, func(other string) bool { return strings.HasPrefix(other, folder+"/") }) {
			return ErrNotEmpty
		}
		i := slices.Index(doc.Folders, folder)
		if i < 0 {
			return ErrNotFound
		}
		doc.Folders = slices.Delete(doc.Folders, i, i+1)
		return nil
	})
}

// MoveFile anota la nueva ruta de un archivo que ya se movió en el bucket.
func (s *Store) MoveFile(ctx context.Context, account string, id int64, path string) (SavedFile, error) {
	var moved SavedFile
	err := s.update(ctx, account, func(doc *document) error {
		i := slices.IndexFunc(doc.Files, func(f SavedFile) bool { return f.ID == id })
		if i < 0 {
			return ErrNotFound
		}
		doc.Files[i].Path = path
		moved = doc.Files[i]
		return nil
	})
	return moved, err
}

// PathTaken dice si otro archivo (distinto de `id`) ya ocupa esa ruta.
func (s *Store) PathTaken(ctx context.Context, account, path string, id int64) (bool, error) {
	doc, err := s.view(ctx, account)
	if err != nil {
		return false, err
	}
	return slices.ContainsFunc(doc.Files, func(f SavedFile) bool { return f.Path == path && f.ID != id }), nil
}
