package invoices

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/Esau21/Go-smpt-dte/internal/bucket"
	"github.com/Esau21/Go-smpt-dte/internal/mailbox"
	"github.com/Esau21/Go-smpt-dte/internal/store"
	"github.com/gin-gonic/gin"
)

// fakeMail simula un buzón: dos páginas de correos, del más nuevo al más viejo.
type fakeMail struct {
	pages   map[string][]string // token -> ids
	next    map[string]string   // token -> siguiente token
	bodies  map[string]string   // id -> contenido del .json ("" = sin adjunto)
	mu      sync.Mutex
	fetched []string
}

var errGone = errors.New("gone")

// fakeBucket es un bucket en memoria.
type fakeBucket struct {
	mu      sync.Mutex
	objects map[string][]byte
}

func (b *fakeBucket) Name() string { return "prueba" }

func (b *fakeBucket) Put(_ context.Context, key string, content []byte, _ string) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.objects[key] = content
	return nil
}

func (b *fakeBucket) Get(_ context.Context, key string) ([]byte, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	content, ok := b.objects[key]
	if !ok {
		return nil, bucket.ErrNotFound
	}
	return content, nil
}

func (b *fakeBucket) Delete(_ context.Context, key string) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	delete(b.objects, key)
	return nil
}

func (f *fakeMail) Account(*gin.Context) (mailbox.Account, bool) {
	return mailbox.Account{Provider: "test", ID: "sync", Email: "t@example.com"}, true
}

func (f *fakeMail) ListDTEMessages(_ *gin.Context, token string, _ int) ([]string, string, error) {
	return f.pages[token], f.next[token], nil
}

func (f *fakeMail) FetchDTE(_ *gin.Context, id string) (*mailbox.Message, error) {
	f.mu.Lock()
	f.fetched = append(f.fetched, id)
	f.mu.Unlock()
	body, ok := f.bodies[id]
	if !ok {
		return nil, errGone
	}
	if body == "" {
		return nil, nil
	}
	return &mailbox.Message{ID: id, From: "proveedor@example.com", Subject: "Su DTE", DTE: []byte(body)}, nil
}

func (f *fakeMail) FetchAttachment(_ *gin.Context, id string, index int) (*mailbox.Attachment, error) {
	if id != "m1" || index != 0 {
		return nil, nil
	}
	return &mailbox.Attachment{MessageID: id, Filename: "factura.pdf", Content: []byte("%PDF-1.4 prueba"), Subject: "Su DTE"}, nil
}

func (f *fakeMail) ErrorStatus(err error) int {
	if err == errGone {
		return 404
	}
	return 502
}

func TestSyncAndRoutes(t *testing.T) {
	dsn := os.Getenv("TEST_DB_STRING")
	if dsn == "" {
		t.Skip("TEST_DB_STRING no definido")
	}
	ctx := context.Background()
	s, err := store.Open(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	const owner = "test:sync"
	_ = s.DeleteOwner(ctx, owner)
	t.Cleanup(func() { _ = s.DeleteOwner(ctx, owner) })

	mail := &fakeMail{
		pages: map[string][]string{"": {"m1", "m2", "m3", "m4"}, "p2": {"m5"}},
		next:  map[string]string{"": "p2"},
		bodies: map[string]string{
			"m1": ccf,
			"m2": factura,
			"m3": `{"no": "es un dte"}`,
			"m4": "",  // correo sin .json
			"m5": ccf, // el mismo DTE reenviado en otro correo
			// m6 no existe: simula un correo borrado
		},
	}
	h := &Handlers{Store: s, Mail: mail}

	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.POST("/sync", h.Sync)
	r.GET("/list", h.List)
	r.GET("/summary", h.Summary)
	r.GET("/export", h.Export)
	r.PATCH("/:id", h.Update)
	r.POST("/files", h.SaveFile)
	r.GET("/files", h.ListFiles)
	r.GET("/files/:id", h.DownloadFile)
	r.DELETE("/files/:id", h.DeleteFile)

	call := func(method, url, body string) (*httptest.ResponseRecorder, map[string]any) {
		req := httptest.NewRequest(method, url, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		var out map[string]any
		_ = json.Unmarshal(w.Body.Bytes(), &out)
		return w, out
	}

	// Primera página: 2 facturas nuevas, 2 correos que no son DTE.
	w, out := call("POST", "/sync", "")
	if w.Code != 200 || out["imported"] != 2.0 || out["skipped"] != 2.0 || out["done"] != false || out["nextPageToken"] != "p2" {
		t.Fatalf("primera página: %d %v", w.Code, out)
	}
	// Segunda página: el DTE repetido no cuenta como nuevo, y ahí termina.
	_, out = call("POST", "/sync?pageToken=p2", "")
	if out["imported"] != 0.0 || out["done"] != true {
		t.Fatalf("segunda página: %v", out)
	}

	// Volver a sincronizar no descarga nada: todo ya estaba revisado.
	mail.fetched = nil
	_, out = call("POST", "/sync", "")
	if len(mail.fetched) != 0 || out["done"] != true {
		t.Errorf("la segunda sincronización no debe descargar correos: %v %v", mail.fetched, out)
	}

	// Un correo borrado a media sincronización no la interrumpe.
	mail.pages[""] = []string{"m6", "m1"}
	if w, out = call("POST", "/sync", ""); w.Code != 200 {
		t.Errorf("correo borrado: %d %v", w.Code, out)
	}

	w, out = call("GET", "/list?from=2026-11-01&to=2026-11-30", "")
	items, _ := out["items"].([]any)
	totals, _ := out["totals"].(map[string]any)
	if w.Code != 200 || len(items) != 2 || totals["total"] != 125.3 {
		t.Fatalf("lista: %d %v", w.Code, out)
	}
	id := items[0].(map[string]any)["id"].(float64)

	if w, _ = call("GET", "/list?from=ayer", ""); w.Code != 400 {
		t.Errorf("una fecha inválida debe rechazarse: %d", w.Code)
	}

	w, out = call("PATCH", "/"+strconv.FormatInt(int64(id), 10), `{"status":"pagada","category":" Servicios "}`)
	if w.Code != 200 || out["status"] != "pagada" || out["category"] != "Servicios" {
		t.Errorf("actualizar: %d %v", w.Code, out)
	}
	if w, _ = call("PATCH", "/1", `{"status":"inventado"}`); w.Code != 400 {
		t.Errorf("un estado inválido debe rechazarse: %d", w.Code)
	}

	w, out = call("GET", "/summary", "")
	if w.Code != 200 || out["byStatus"].(map[string]any)["pagada"] != 1.0 {
		t.Errorf("resumen: %d %v", w.Code, out)
	}

	for _, kind := range []string{"compras", "detalle"} {
		w, _ = call("GET", "/export?kind="+kind+"&from=2026-11-01&to=2026-11-30", "")
		isXlsx := strings.HasPrefix(w.Body.String(), "PK")
		if w.Code != http.StatusOK || !isXlsx || !strings.Contains(w.Header().Get("Content-Disposition"), ".xlsx") {
			t.Errorf("exportar %s: %d xlsx=%v %q", kind, w.Code, isXlsx, w.Header().Get("Content-Disposition"))
		}
	}

	// Un archivo guardado antes de tener bucket (queda en la base de datos)...
	if w, out = call("POST", "/files", `{"messageId":"m1","index":0}`); w.Code != http.StatusCreated || out["path"] != "" {
		t.Fatalf("guardar sin bucket: %d %v", w.Code, out)
	}
	// ...pasa solo al bucket, a su carpeta ordenada, cuando el bucket existe.
	files := &fakeBucket{objects: map[string][]byte{}}
	h.Bucket = files
	const wantKey = "t@example.com/2026/11/Proveedor de Prueba, S.A. de C.V/factura.pdf"
	_, out = call("GET", "/files", "")
	if listed := out["files"].([]any); len(listed) != 1 || listed[0].(map[string]any)["path"] != wantKey {
		t.Fatalf("migración al bucket: %v", out)
	}
	if string(files.objects[wantKey]) != "%PDF-1.4 prueba" {
		t.Fatalf("el contenido no llegó al bucket: %v", files.objects)
	}

	// Guardar un adjunto en la nube, dos veces: no debe duplicarse.
	for range 2 {
		if w, out = call("POST", "/files", `{"messageId":"m1","index":0}`); w.Code != http.StatusCreated || out["filename"] != "factura.pdf" {
			t.Fatalf("guardar archivo: %d %v", w.Code, out)
		}
	}
	fileID := strconv.FormatInt(int64(out["id"].(float64)), 10)
	if _, out = call("GET", "/files?messageId=m1", ""); len(out["files"].([]any)) != 1 {
		t.Errorf("lista de archivos: %v", out)
	}
	if w, _ = call("GET", "/files/"+fileID, ""); w.Body.String() != "%PDF-1.4 prueba" || w.Header().Get("Content-Type") != "application/pdf" {
		t.Errorf("descargar archivo: %d %q %q", w.Code, w.Body.String(), w.Header().Get("Content-Type"))
	}
	if w, _ = call("POST", "/files", `{"messageId":"m1","index":7}`); w.Code != 404 {
		t.Errorf("un adjunto que no existe: %d", w.Code)
	}
	if _, ok := files.objects[wantKey]; !ok || len(files.objects) != 1 {
		t.Errorf("el bucket debe tener solo %q: %v", wantKey, files.objects)
	}
	if w, _ = call("DELETE", "/files/"+fileID, ""); w.Code != http.StatusNoContent {
		t.Errorf("borrar archivo: %d", w.Code)
	}
	if len(files.objects) != 0 {
		t.Errorf("al borrar el archivo también debe borrarse del bucket: %v", files.objects)
	}
	if w, _ = call("GET", "/files/"+fileID, ""); w.Code != 404 {
		t.Errorf("el archivo borrado ya no debe existir: %d", w.Code)
	}

	// Sin base de datos las rutas avisan en vez de fallar.
	r2 := gin.New()
	r2.GET("/list", (&Handlers{Mail: mail}).List)
	w2 := httptest.NewRecorder()
	r2.ServeHTTP(w2, httptest.NewRequest("GET", "/list", nil))
	if w2.Code != http.StatusServiceUnavailable {
		t.Errorf("sin base de datos: %d", w2.Code)
	}
}
