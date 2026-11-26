// Package bucket guarda archivos en un bucket de Google Cloud Storage.
//
// En desarrollo se usa un emulador local: basta con definir
// STORAGE_EMULATOR_HOST (lo hace compose.yml). En producción esa variable no
// existe y la librería usa las credenciales de Google del entorno
// (GOOGLE_APPLICATION_CREDENTIALS o la cuenta de servicio de la máquina).
package bucket

import (
	"context"
	"errors"
	"io"
	"os"
	"strings"
	"time"
	"unicode"

	"cloud.google.com/go/storage"
)

type Bucket struct {
	name   string
	handle *storage.BucketHandle
}

// Open abre el bucket. Con el emulador local lo crea si no existe; en Google
// Cloud el bucket ya debe existir.
func Open(ctx context.Context, name string) (*Bucket, error) {
	// WithJSONReads hace las descargas por la misma API que las subidas. Así
	// funcionan igual en Google Cloud y en el emulador, que solo atiende la
	// otra vía de descarga si el nombre del servidor coincide exactamente.
	client, err := storage.NewClient(ctx, storage.WithJSONReads())
	if err != nil {
		return nil, err
	}
	handle := client.Bucket(name)

	if os.Getenv("STORAGE_EMULATOR_HOST") != "" {
		// Con límite de tiempo: si el emulador no responde (por ejemplo, al
		// correr la API fuera de Docker) la librería reintentaría sin fin y
		// la API nunca terminaría de arrancar.
		ctx, cancel := context.WithTimeout(ctx, 6*time.Second)
		defer cancel()

		_, err := handle.Attrs(ctx)
		if errors.Is(err, storage.ErrBucketNotExist) {
			err = handle.Create(ctx, "local", nil)
		}
		if err != nil {
			return nil, err
		}
	}
	return &Bucket{name: name, handle: handle}, nil
}

func (b *Bucket) Name() string { return b.name }

func (b *Bucket) Put(ctx context.Context, key string, content []byte, contentType string) error {
	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()

	w := b.handle.Object(key).NewWriter(ctx)
	w.ContentType = contentType
	if _, err := w.Write(content); err != nil {
		_ = w.Close()
		return err
	}
	return w.Close()
}

// ErrNotFound indica que el objeto no está en el bucket.
var ErrNotFound = errors.New("object not found")

func (b *Bucket) Get(ctx context.Context, key string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()

	r, err := b.handle.Object(key).NewReader(ctx)
	if errors.Is(err, storage.ErrObjectNotExist) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	defer r.Close()
	return io.ReadAll(r)
}

// Delete borra un objeto. Que ya no exista no es un error.
func (b *Bucket) Delete(ctx context.Context, key string) error {
	err := b.handle.Object(key).Delete(ctx)
	if errors.Is(err, storage.ErrObjectNotExist) {
		return nil
	}
	return err
}

// Key arma la ruta ordenada de un archivo dentro del bucket:
//
//	cuenta/AAAA/MM/Emisor/archivo.pdf
//
// `fecha` es la fecha del documento (AAAA-MM-DD). Si el archivo no
// pertenece a una factura conocida (fecha vacía) va a "sin-clasificar".
func Key(account, fecha, emisor, filename string) string {
	parts := []string{segment(account)}
	if len(fecha) >= 7 {
		if emisor == "" {
			emisor = "Sin emisor"
		}
		parts = append(parts, fecha[:4], fecha[5:7], segment(emisor))
	} else {
		parts = append(parts, "sin-clasificar")
	}
	return strings.Join(append(parts, segment(filename)), "/")
}

// segment limpia un texto para usarlo como nombre de carpeta o de archivo:
// sin barras ni caracteres de control, sin espacios repetidos y sin puntos
// al final.
func segment(text string) string {
	var b strings.Builder
	space := false
	for _, r := range strings.TrimSpace(text) {
		switch {
		case r == '/' || r == '\\' || unicode.IsControl(r):
			r = '-'
		case unicode.IsSpace(r):
			if space {
				continue
			}
			r = ' '
		}
		space = r == ' '
		b.WriteRune(r)
	}

	out := strings.TrimRight(b.String(), ". ")
	if runes := []rune(out); len(runes) > 80 {
		out = strings.TrimRight(string(runes[:80]), ". ")
	}
	if out == "" || out == "." || out == ".." {
		return "sin-nombre"
	}
	return out
}
