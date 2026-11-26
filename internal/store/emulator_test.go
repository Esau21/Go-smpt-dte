package store

import (
	"context"
	"os"
	"testing"

	"github.com/Esau21/Go-smpt-dte/internal/bucket"
)

// TestEmulatorData lee los datos reales de una cuenta en el emulador local.
// Se omite salvo que se indiquen el emulador, el bucket y la cuenta:
//
//	STORAGE_EMULATOR_HOST=localhost:4443 FILES_BUCKET=bookeep-archivos TEST_ACCOUNT=correo@ejemplo.com
func TestEmulatorData(t *testing.T) {
	account := os.Getenv("TEST_ACCOUNT")
	if os.Getenv("STORAGE_EMULATOR_HOST") == "" || account == "" {
		t.Skip("STORAGE_EMULATOR_HOST o TEST_ACCOUNT no definidos")
	}
	ctx := context.Background()
	b, err := bucket.Open(ctx, os.Getenv("FILES_BUCKET"))
	if err != nil {
		t.Fatal(err)
	}
	s := New(b)

	items, totals, err := s.List(ctx, account, Filter{})
	if err != nil {
		t.Fatal(err)
	}
	files, err := s.ListFiles(ctx, account, "")
	if err != nil {
		t.Fatal(err)
	}
	summary, err := s.Summary(ctx, account, "2025-12-01")
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("facturas: %d (total %.2f) | archivos: %d | estados: %v | meses: %d",
		len(items), totals.Total, len(files), summary.ByStatus, len(summary.Months))
	if len(items) == 0 {
		t.Error("la cuenta no tiene facturas")
	}
	for _, f := range files {
		if f.CreatedAt.IsZero() || f.Path == "" {
			t.Errorf("archivo mal leído: %+v", f)
		}
	}
}
