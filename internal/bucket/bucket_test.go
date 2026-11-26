package bucket

import "testing"

func TestKey(t *testing.T) {
	cases := []struct {
		name                         string
		account, fecha, emisor, file string
		want                         string
	}{
		{"factura conocida", "ana@example.com", "2026-11-20", "ALMACENES SIMAN S.A. DE C.V.", "ABC.pdf",
			"ana@example.com/2026/11/ALMACENES SIMAN S.A. DE C.V/ABC.pdf"},
		{"sin factura", "ana@example.com", "", "", "contrato.pdf",
			"ana@example.com/sin-clasificar/contrato.pdf"},
		{"factura sin emisor", "ana@example.com", "2026-01-05", "", "x.json",
			"ana@example.com/2026/01/Sin emisor/x.json"},
		{"nombres con barras y espacios", "ana@example.com", "2026-02-01", "  A/B   C\\D  ", "../../etc/passwd",
			"ana@example.com/2026/02/A-B C-D/..-..-etc-passwd"},
		{"nombre vacío", "ana@example.com", "2026-02-01", "X", "..",
			"ana@example.com/2026/02/X/sin-nombre"},
	}
	for _, c := range cases {
		if got := Key(c.account, c.fecha, c.emisor, c.file); got != c.want {
			t.Errorf("%s:\n obtuve  %q\n esperaba %q", c.name, got, c.want)
		}
	}
}
