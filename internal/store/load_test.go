package store

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/Esau21/Go-smpt-dte/internal/bucket"
)

// countingBlob es un bucket en memoria que cuenta cuántas veces se lee.
type countingBlob struct {
	mu      sync.Mutex
	objects map[string][]byte
	gets    int
}

func (b *countingBlob) Put(_ context.Context, key string, content []byte, _ string) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.objects[key] = content
	return nil
}

func (b *countingBlob) Get(_ context.Context, key string) ([]byte, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.gets++
	content, ok := b.objects[key]
	if !ok {
		return nil, bucket.ErrNotFound
	}
	return content, nil
}

func (b *countingBlob) Delete(_ context.Context, key string) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	delete(b.objects, key)
	return nil
}

// TestManyInvoices comprueba que una cuenta con 20 000 facturas se puede
// sincronizar, listar y resumir en tiempos razonables, y que las consultas
// no vuelven a bajar el documento del bucket cada vez.
func TestManyInvoices(t *testing.T) {
	const account = "muchas@example.com"
	const total, page = 20000, 50
	ctx := context.Background()
	blob := &countingBlob{objects: map[string][]byte{}}
	s := New(blob)

	start := time.Now()
	for from := 0; from < total; from += 2000 {
		// Se cargan en tandas grandes para que la prueba sea rápida; cada
		// tanda equivale a muchas páginas de sincronización.
		var found []Invoice
		var scanned []string
		for n := from; n < from+2000; n++ {
			found = append(found, Invoice{
				CodigoGeneracion: fmt.Sprintf("CODIGO-%06d", n),
				TipoDte:          []string{"01", "03", "05"}[n%3],
				Fecha:            fmt.Sprintf("2026-%02d-%02d", n%12+1, n%28+1),
				EmisorNIT:        fmt.Sprintf("NIT-%03d", n%400),
				EmisorNombre:     fmt.Sprintf("Proveedor %03d, S.A. de C.V.", n%400),
				Gravado:          100, IVA: 13, Total: 113,
				MessageID: fmt.Sprintf("m%06d", n),
			})
			scanned = append(scanned, fmt.Sprintf("m%06d", n))
		}
		if imported, err := s.SaveSync(ctx, account, found, scanned); err != nil || imported != 2000 {
			t.Fatalf("carga: %d %v", imported, err)
		}
	}
	t.Logf("cargar %d facturas: %v (documento de %.1f MB)", total, time.Since(start).Round(time.Millisecond),
		float64(len(blob.objects[bucket.DataKey(account)]))/1e6)

	// Una página más de sincronización sobre la cuenta ya cargada.
	start = time.Now()
	extra := []Invoice{{CodigoGeneracion: "NUEVA", Fecha: "2026-12-31", Total: 1, MessageID: "nuevo"}}
	if imported, err := s.SaveSync(ctx, account, append(extra, Invoice{CodigoGeneracion: "CODIGO-000007"}), []string{"nuevo"}); err != nil || imported != 1 {
		t.Fatalf("página extra: %d %v", imported, err)
	}
	sync := time.Since(start)

	blob.gets = 0
	start = time.Now()
	for range 20 {
		items, totals, err := s.List(ctx, account, Filter{From: "2026-06-01", Search: "proveedor 01", Limit: page})
		if err != nil || len(items) != page || totals.Count == 0 {
			t.Fatalf("lista: %d %+v %v", len(items), totals, err)
		}
		ids := []string{"m000001", "m019999", "no-existe"}
		if known, err := s.Known(ctx, account, ids); err != nil || len(known) != 2 {
			t.Fatalf("correos revisados: %v %v", known, err)
		}
	}
	list := time.Since(start) / 20

	start = time.Now()
	summary, err := s.Summary(ctx, account, "2026-01-01")
	if err != nil || len(summary.Months) != 12 || len(summary.Issuers) != 6 || len(summary.Recent) != 6 {
		t.Fatalf("resumen: %d meses, %d emisores, %d recientes, %v", len(summary.Months), len(summary.Issuers), len(summary.Recent), err)
	}
	t.Logf("página de sincronización: %v | lista filtrada: %v | resumen: %v | lecturas del bucket en 41 consultas: %d",
		sync.Round(time.Millisecond), list.Round(time.Microsecond), time.Since(start).Round(time.Millisecond), blob.gets)

	if blob.gets != 0 {
		t.Errorf("las consultas deben salir de la memoria, no del bucket: %d lecturas", blob.gets)
	}
	if sync > 3*time.Second || list > 500*time.Millisecond {
		t.Errorf("demasiado lento: sincronización %v, lista %v", sync, list)
	}

	// Lo que se cambia se ve de inmediato, y lo que anotó el usuario no se
	// pierde al volver a sincronizar el mismo DTE.
	status := "pagada"
	if _, err := s.Update(ctx, account, 8, Patch{Status: &status}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SaveSync(ctx, account, []Invoice{{CodigoGeneracion: "CODIGO-000007", Total: 500}}, nil); err != nil {
		t.Fatal(err)
	}
	paid, _, _ := s.List(ctx, account, Filter{Status: "pagada"})
	if len(paid) != 1 || paid[0].CodigoGeneracion != "CODIGO-000007" || paid[0].Total != 500 {
		t.Errorf("el estado debe conservarse y los montos actualizarse: %+v", paid)
	}

	// Una cuenta guardada antes de comprimir el documento se sigue leyendo,
	// y al primer cambio pasa al formato nuevo.
	const old = "antigua@example.com"
	blob.objects[bucket.LegacyDataKey(old)] = []byte(`{"nextInvoiceId":5,"nextFileId":1,"invoices":[{"id":4,"codigoGeneracion":"VIEJA","status":"pagada","total":10}]}`)
	if items, _, err := s.List(ctx, old, Filter{}); err != nil || len(items) != 1 || items[0].Status != "pagada" {
		t.Fatalf("documento sin comprimir: %+v %v", items, err)
	}
	if imported, err := s.SaveSync(ctx, old, []Invoice{{CodigoGeneracion: "OTRA"}}, nil); err != nil || imported != 1 {
		t.Fatal(imported, err)
	}
	if _, legacy := blob.objects[bucket.LegacyDataKey(old)]; legacy || blob.objects[bucket.DataKey(old)] == nil {
		t.Error("tras el primer cambio debe quedar solo el documento comprimido")
	}
	if items, _, _ := New(blob).List(ctx, old, Filter{}); len(items) != 2 || items[0].ID+items[1].ID != 9 {
		t.Errorf("las facturas anteriores deben conservarse y la nueva seguir la numeración: %+v", items)
	}

	// Otra copia de la API (otro Store sobre el mismo bucket) ve lo guardado.
	if other, _, _ := New(blob).List(ctx, account, Filter{Status: "pagada"}); len(other) != 1 {
		t.Errorf("lo guardado debe estar en el bucket, no solo en memoria: %d", len(other))
	}
}
