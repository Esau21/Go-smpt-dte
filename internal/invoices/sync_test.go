package invoices

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/Esau21/Go-smpt-dte/internal/bucket"
	"github.com/Esau21/Go-smpt-dte/internal/mailbox"
	"github.com/gin-gonic/gin"
)

// fakeBucket es un bucket en memoria.
type fakeBucket struct {
	mu      sync.Mutex
	objects map[string][]byte
}

func newFakeBucket() *fakeBucket { return &fakeBucket{objects: map[string][]byte{}} }

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

// fakeMail simula un buzón: dos páginas de correos, del más nuevo al más viejo.
type fakeMail struct {
	pages  map[string][]string // token -> ids
	next   map[string]string   // token -> siguiente token
	bodies map[string]string   // id -> contenido del .json ("" = sin adjunto)

	mu      sync.Mutex
	fetched []string
}

var errGone = errors.New("gone")

const testAccount = "t@example.com"

func (f *fakeMail) Account(*gin.Context) (mailbox.Account, bool) {
	return mailbox.Account{Provider: "test", ID: "sync", Email: testAccount}, true
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
	files := newFakeBucket()
	h := New(files, mail)

	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.POST("/sync", h.Sync)
	r.GET("/list", h.List)
	r.GET("/summary", h.Summary)
	r.GET("/export", h.Export)
	r.PATCH("/:id", h.Update)
	r.PATCH("/bulk", h.UpdateMany)
	r.POST("/files", h.SaveFile)
	r.GET("/files", h.ListFiles)
	r.GET("/files/:id", h.DownloadFile)
	r.DELETE("/files/:id", h.DeleteFile)
	r.PATCH("/files/:id", h.MoveFile)
	r.POST("/folders", h.CreateFolder)
	r.DELETE("/folders", h.DeleteFolder)

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
	// Todo quedó en un solo documento dentro de la carpeta de la cuenta.
	if _, ok := files.objects[bucket.DataKey(testAccount)]; !ok || len(files.objects) != 1 {
		t.Fatalf("los datos deben estar en %q: %d objetos", bucket.DataKey(testAccount), len(files.objects))
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
	if w.Code != 200 || len(items) != 2 || totals["total"] != 125.3 || totals["count"] != 2.0 {
		t.Fatalf("lista: %d %v", w.Code, out)
	}
	// La más reciente primero: la factura del 21, luego el crédito fiscal del 20.
	first := items[0].(map[string]any)
	if first["fecha"] != "2026-11-21" || first["status"] != "pendiente" {
		t.Errorf("orden o estado inicial: %v", first)
	}
	id := strconv.FormatInt(int64(first["id"].(float64)), 10)

	if _, out = call("GET", "/list?q=PROVEEDOR&tipo=03&limit=1", ""); len(out["items"].([]any)) != 1 {
		t.Errorf("filtros: %v", out)
	}
	if w, _ = call("GET", "/list?from=ayer", ""); w.Code != 400 {
		t.Errorf("una fecha inválida debe rechazarse: %d", w.Code)
	}

	w, out = call("PATCH", "/"+id, `{"status":"pagada","category":" Servicios ","notes":"Pagada por transferencia"}`)
	if w.Code != 200 || out["status"] != "pagada" || out["category"] != "Servicios" {
		t.Errorf("actualizar: %d %v", w.Code, out)
	}
	if w, _ = call("PATCH", "/"+id, `{"status":"inventado"}`); w.Code != 400 {
		t.Errorf("un estado inválido debe rechazarse: %d", w.Code)
	}
	if w, _ = call("PATCH", "/999", `{"status":"pagada"}`); w.Code != 404 {
		t.Errorf("una factura que no existe: %d", w.Code)
	}
	// Lo que anotó el usuario sigue ahí al volver a leer.
	_, out = call("GET", "/list?status=pagada", "")
	if paid := out["items"].([]any); len(paid) != 1 || paid[0].(map[string]any)["notes"] != "Pagada por transferencia" {
		t.Errorf("el estado y las notas deben conservarse: %v", out)
	}

	// Ordenar por total y contar por estado (el conteo ignora el estado elegido).
	_, out = call("GET", "/list?sort=total_desc&status=pagada", "")
	counts := out["byStatus"].(map[string]any)
	if counts["pagada"] != 1.0 || counts["pendiente"] != 1.0 || len(out["items"].([]any)) != 1 {
		t.Errorf("conteo por estado: %v", out)
	}
	_, out = call("GET", "/list?sort=total_desc", "")
	if sorted := out["items"].([]any); sorted[0].(map[string]any)["total"] != 114.0 {
		t.Errorf("orden por total: %v", sorted)
	}
	if w, _ = call("GET", "/list?sort=inventado", ""); w.Code != 400 {
		t.Errorf("un orden inválido debe rechazarse: %d", w.Code)
	}

	// Cambiar varias a la vez, y dejarlas como estaban.
	if w, out = call("PATCH", "/bulk", `{"ids":[1,2,999],"status":"revisada"}`); w.Code != 200 || out["updated"] != 2.0 {
		t.Errorf("cambio en lote: %d %v", w.Code, out)
	}
	if w, _ = call("PATCH", "/bulk", `{"ids":[],"status":"pagada"}`); w.Code != 400 {
		t.Errorf("un lote vacío debe rechazarse: %d", w.Code)
	}
	call("PATCH", "/bulk", `{"ids":[`+id+`],"status":"pagada"}`)
	otherID := "1"
	if id == "1" {
		otherID = "2"
	}
	call("PATCH", "/bulk", `{"ids":[`+otherID+`],"status":"pendiente"}`)

	w, out = call("GET", "/summary", "")
	if recent := out["recent"].([]any); len(recent) != 2 || recent[0].(map[string]any)["fecha"] != "2026-11-21" {
		t.Errorf("últimas facturas: %v", out["recent"])
	}
	if types := out["types"].([]any); len(types) != 2 || types[0].(map[string]any)["name"] != "03" {
		t.Errorf("por tipo de documento: %v", out["types"])
	}
	// La factura pagada (11.30) no cuenta como pendiente; el crédito fiscal (114), sí.
	if out["pending"].(map[string]any)["total"] != 114.0 {
		t.Errorf("pendiente de pago: %v", out["pending"])
	}
	months, _ := out["months"].([]any)
	if w.Code != 200 || out["byStatus"].(map[string]any)["pagada"] != 1.0 || len(months) != 1 ||
		months[0].(map[string]any)["total"] != 125.3 || len(out["issuers"].([]any)) != 1 {
		t.Errorf("resumen: %d %v", w.Code, out)
	}

	for _, kind := range []string{"compras", "detalle"} {
		w, _ = call("GET", "/export?kind="+kind+"&from=2026-11-01&to=2026-11-30", "")
		isXlsx := strings.HasPrefix(w.Body.String(), "PK")
		if w.Code != http.StatusOK || !isXlsx || !strings.Contains(w.Header().Get("Content-Disposition"), ".xlsx") {
			t.Errorf("exportar %s: %d xlsx=%v %q", kind, w.Code, isXlsx, w.Header().Get("Content-Disposition"))
		}
	}

	// Guardar un adjunto en la nube, dos veces: no debe duplicarse. Va a la
	// carpeta de la factura que llegó en ese correo.
	const wantKey = testAccount + "/2026/11/Proveedor de Prueba, S.A. de C.V/factura.pdf"
	for range 2 {
		if w, out = call("POST", "/files", `{"messageId":"m1","index":0}`); w.Code != http.StatusCreated || out["path"] != wantKey {
			t.Fatalf("guardar archivo: %d %v", w.Code, out)
		}
	}
	fileID := strconv.FormatInt(int64(out["id"].(float64)), 10)
	if string(files.objects[wantKey]) != "%PDF-1.4 prueba" || len(files.objects) != 2 {
		t.Errorf("el bucket debe tener los datos y un archivo: %d objetos", len(files.objects))
	}
	if _, out = call("GET", "/files?messageId=m1", ""); len(out["files"].([]any)) != 1 || out["bucket"] != "prueba" {
		t.Errorf("lista de archivos: %v", out)
	}
	if w, _ = call("GET", "/files/"+fileID, ""); w.Body.String() != "%PDF-1.4 prueba" || w.Header().Get("Content-Type") != "application/pdf" {
		t.Errorf("descargar archivo: %d %q %q", w.Code, w.Body.String(), w.Header().Get("Content-Type"))
	}
	if w, _ = call("POST", "/files", `{"messageId":"m1","index":7}`); w.Code != 404 {
		t.Errorf("un adjunto que no existe: %d", w.Code)
	}
	if w, _ = call("DELETE", "/files/"+fileID, ""); w.Code != http.StatusNoContent {
		t.Errorf("borrar archivo: %d", w.Code)
	}
	if _, ok := files.objects[wantKey]; ok {
		t.Error("al borrar el archivo también debe borrarse del bucket")
	}
	if w, _ = call("GET", "/files/"+fileID, ""); w.Code != 404 {
		t.Errorf("el archivo borrado ya no debe existir: %d", w.Code)
	}

	// --- Carpetas elegidas por el usuario ---

	// Una carpeta creada a mano existe aunque esté vacía.
	if w, out = call("POST", "/folders", `{"path":" Clientes / Importantes "}`); w.Code != http.StatusCreated || out["path"] != "Clientes/Importantes" {
		t.Fatalf("crear carpeta: %d %v", w.Code, out)
	}
	if _, out = call("GET", "/files", ""); len(out["folders"].([]any)) != 1 {
		t.Errorf("la carpeta vacía debe listarse: %v", out)
	}
	if w, _ = call("POST", "/folders", `{"path":"_bookeep/x"}`); w.Code != 400 {
		t.Errorf("la carpeta de datos de la app no se puede usar: %d", w.Code)
	}
	if w, _ = call("POST", "/folders", `{"path":"a/b/c/d/e/f/g"}`); w.Code != 400 {
		t.Errorf("demasiadas carpetas anidadas: %d", w.Code)
	}

	// Guardar eligiendo carpeta.
	const custom = testAccount + "/Clientes/Importantes/factura.pdf"
	if w, out = call("POST", "/files", `{"messageId":"m1","index":0,"folder":"Clientes/Importantes"}`); w.Code != http.StatusCreated || out["path"] != custom {
		t.Fatalf("guardar en carpeta elegida: %d %v", w.Code, out)
	}
	movedID := strconv.FormatInt(int64(out["id"].(float64)), 10)

	// Guardar el mismo adjunto en otra carpeta lo cambia de lugar, no lo duplica.
	const other = testAccount + "/Otra/factura.pdf"
	if w, out = call("POST", "/files", `{"messageId":"m1","index":0,"folder":"Otra"}`); w.Code != http.StatusCreated || out["path"] != other {
		t.Fatalf("guardar de nuevo en otra carpeta: %d %v", w.Code, out)
	}
	if _, stillThere := files.objects[custom]; stillThere || string(files.objects[other]) != "%PDF-1.4 prueba" {
		t.Errorf("el archivo debe estar solo en la carpeta nueva")
	}

	// Mover un archivo guardado.
	if w, out = call("PATCH", "/files/"+movedID, `{"folder":"Clientes/Importantes"}`); w.Code != 200 || out["path"] != custom {
		t.Fatalf("mover archivo: %d %v", w.Code, out)
	}
	if _, old := files.objects[other]; old || string(files.objects[custom]) != "%PDF-1.4 prueba" {
		t.Errorf("al mover, el archivo debe quedar solo en el destino")
	}
	if w, _ = call("GET", "/files/"+movedID, ""); w.Body.String() != "%PDF-1.4 prueba" {
		t.Errorf("el archivo movido debe poder abrirse: %d", w.Code)
	}

	// Una carpeta con archivos no se puede borrar; vacía, sí.
	if w, _ = call("DELETE", "/folders?path=Clientes/Importantes", ""); w.Code != http.StatusConflict {
		t.Errorf("borrar carpeta con archivos: %d", w.Code)
	}
	if w, _ = call("PATCH", "/files/"+movedID, `{"folder":""}`); w.Code != 200 {
		t.Errorf("mover a la raíz: %d", w.Code)
	}
	if w, _ = call("DELETE", "/folders?path=Clientes/Importantes", ""); w.Code != http.StatusNoContent {
		t.Errorf("borrar carpeta vacía: %d", w.Code)
	}

	// Sin bucket las rutas avisan en vez de fallar.
	r2 := gin.New()
	r2.GET("/list", New(nil, mail).List)
	w2 := httptest.NewRecorder()
	r2.ServeHTTP(w2, httptest.NewRequest("GET", "/list", nil))
	if w2.Code != http.StatusServiceUnavailable {
		t.Errorf("sin bucket: %d", w2.Code)
	}
}
