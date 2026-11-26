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

func TestCleanFolder(t *testing.T) {
	cases := []struct {
		in   string
		want string
		ok   bool
	}{
		{"", "", true},
		{" Clientes / 2026 //Siman ", "Clientes/2026/Siman", true},
		{"a\\b", "a/b", true},
		{"../../secreto", "sin-nombre/sin-nombre/secreto", true},
		{"_bookeep", "", false},
		{"1/2/3/4/5/6/7", "", false},
	}
	for _, c := range cases {
		got, ok := CleanFolder(c.in)
		if got != c.want || ok != c.ok {
			t.Errorf("CleanFolder(%q) = %q, %v; esperaba %q, %v", c.in, got, ok, c.want, c.ok)
		}
	}
	if got := KeyIn("ana@example.com", "", "a.pdf"); got != "ana@example.com/a.pdf" {
		t.Errorf("KeyIn raíz: %q", got)
	}
}
