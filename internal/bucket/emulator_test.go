package bucket

import (
	"context"
	"os"
	"testing"
)

// TestEmulator prueba subir, leer y borrar contra el emulador local. Se
// omite si no está definido: STORAGE_EMULATOR_HOST=localhost:4443
func TestEmulator(t *testing.T) {
	if os.Getenv("STORAGE_EMULATOR_HOST") == "" {
		t.Skip("STORAGE_EMULATOR_HOST no definido")
	}
	ctx := context.Background()
	b, err := Open(ctx, "bookeep-pruebas")
	if err != nil {
		t.Fatal(err)
	}

	key := Key("prueba@example.com", "2026-11-20", "Proveedor de Prueba", "factura ñ.pdf")
	if err := b.Put(ctx, key, []byte("contenido"), "application/pdf"); err != nil {
		t.Fatal(err)
	}
	got, err := b.Get(ctx, key)
	if err != nil || string(got) != "contenido" {
		t.Fatalf("leer: %q %v", got, err)
	}
	if err := b.Delete(ctx, key); err != nil {
		t.Fatal(err)
	}
	if _, err := b.Get(ctx, key); err != ErrNotFound {
		t.Errorf("un objeto borrado debe dar ErrNotFound: %v", err)
	}
	if err := b.Delete(ctx, key); err != nil {
		t.Errorf("borrar dos veces no debe fallar: %v", err)
	}
}
