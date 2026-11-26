package invoices

import (
	"bytes"
	"strings"
	"testing"

	"github.com/Esau21/Go-smpt-dte/internal/mailbox"
	"github.com/Esau21/Go-smpt-dte/internal/store"
	"github.com/xuri/excelize/v2"
)

// Un comprobante de crédito fiscal como los que emite Hacienda.
const ccf = `{
  "identificacion": {"version": 3, "ambiente": "01", "tipoDte": "03",
    "numeroControl": "DTE-03-M001P001-000000000000123",
    "codigoGeneracion": "a1b2c3d4-0000-4000-8000-000000000001",
    "fecEmi": "2026-11-20", "horEmi": "10:15:00", "tipoMoneda": "USD"},
  "emisor": {"nit": "06140101001010", "nrc": "123456", "nombre": "Proveedor de Prueba, S.A. de C.V."},
  "receptor": {"nit": "06142002002020", "nrc": "654321", "nombre": "Cliente de Prueba, S.A. de C.V."},
  "cuerpoDocumento": [{"numItem": 1, "descripcion": "Servicio", "cantidad": 1, "precioUni": 100, "ventaGravada": 100}],
  "resumen": {"totalNoSuj": 0, "totalExenta": 0, "totalGravada": 100,
    "tributos": [{"codigo": "20", "descripcion": "IVA 13%", "valor": 13}],
    "subTotal": 100, "ivaPerci1": 1, "ivaRete1": 0, "montoTotalOperacion": 113, "totalPagar": 114},
  "respuestaHacienda": {"selloRecibido": "2026ABCDEF"}
}`

// Una factura de consumidor final: el IVA viene incluido y se informa aparte.
const factura = `{
  "identificacion": {"tipoDte": "01", "codigoGeneracion": "A1B2C3D4-0000-4000-8000-000000000002", "fecEmi": "2026-11-21"},
  "emisor": {"nit": "06140101001010", "nombre": "Proveedor de Prueba, S.A. de C.V."},
  "receptor": {"numDocumento": "012345678", "nombre": "Persona de Prueba"},
  "resumen": {"totalGravada": 11.3, "totalIva": 1.3, "totalPagar": 11.3},
  "selloRecibido": "2026SELLO"
}`

func TestToInvoice(t *testing.T) {
	got, ok := toInvoice(&mailbox.Message{ID: "m1", From: "a@b.c", Subject: "DTE", DTE: []byte(ccf)})
	if !ok {
		t.Fatal("el crédito fiscal debería reconocerse como DTE")
	}
	checks := map[string][2]any{
		"codigo en mayúsculas": {got.CodigoGeneracion, "A1B2C3D4-0000-4000-8000-000000000001"},
		"fecha":                {got.Fecha, "2026-11-20"},
		"emisor":               {got.EmisorNRC, "123456"},
		"receptor":             {got.ReceptorDoc, "06142002002020"},
		"gravado":              {got.Gravado, 100.0},
		"iva de tributos":      {got.IVA, 13.0},
		"iva percibido":        {got.IVAPercibido, 1.0},
		"total":                {got.Total, 114.0},
		"sello alterno":        {got.Sello, "2026ABCDEF"},
		"correo":               {got.MessageID, "m1"},
	}
	for name, c := range checks {
		if c[0] != c[1] {
			t.Errorf("%s: obtuve %v, esperaba %v", name, c[0], c[1])
		}
	}

	got, ok = toInvoice(&mailbox.Message{DTE: []byte(factura)})
	if !ok || got.IVA != 1.3 || got.ReceptorDoc != "012345678" || got.Sello != "2026SELLO" || got.Moneda != "USD" {
		t.Errorf("factura mal leída: %+v", got)
	}

	for name, body := range map[string]string{
		"json que no es DTE": `{"hola": "mundo"}`,
		"no es json":         `<html></html>`,
	} {
		if _, ok := toInvoice(&mailbox.Message{DTE: []byte(body)}); ok {
			t.Errorf("%s no debería reconocerse como DTE", name)
		}
	}
	// Un .json en Windows-1252 (Ñ = 0xD1) y con BOM debe leerse bien.
	latin := bytes.Replace([]byte(factura), []byte("Proveedor de Prueba"), append([]byte("BANCO SALVADORE"), 0xD1, 'O'), 1)
	got, ok = toInvoice(&mailbox.Message{DTE: latin})
	if !ok || got.EmisorNombre != "BANCO SALVADOREÑO, S.A. de C.V." {
		t.Errorf("Windows-1252: %q", got.EmisorNombre)
	}
	// Texto que el emisor mandó ya dañado: "COMPAÃ‘IA" debe quedar "COMPAÑIA",
	// sin estropear una Ñ correcta en el mismo archivo.
	broken := strings.Replace(factura, "Proveedor de Prueba", "COMPAÃ‘IA Y DISEÑO MÃ©XICO", 1)
	got, ok = toInvoice(&mailbox.Message{DTE: []byte(broken)})
	if !ok || got.EmisorNombre != "COMPAÑIA Y DISEÑO MéXICO, S.A. de C.V." {
		t.Errorf("texto dañado: %q", got.EmisorNombre)
	}
	// Un archivo que mezcla UTF-8 correcto con un byte suelto de Windows-1252.
	mixed := bytes.Replace([]byte(factura), []byte("Proveedor de Prueba"), append([]byte("DISEÑO SALVADORE"), 0xD1, 'O'), 1)
	got, ok = toInvoice(&mailbox.Message{DTE: mixed})
	if !ok || got.EmisorNombre != "DISEÑO SALVADOREÑO, S.A. de C.V." {
		t.Errorf("archivo mezclado: %q", got.EmisorNombre)
	}

	withBOM := append([]byte{0xEF, 0xBB, 0xBF}, factura...)
	if _, ok = toInvoice(&mailbox.Message{DTE: withBOM}); !ok {
		t.Error("un .json con BOM debe reconocerse")
	}

	if _, ok := toInvoice(nil); ok {
		t.Error("un correo sin adjunto no es un DTE")
	}
}

func sample() []store.Invoice {
	ccfInvoice, _ := toInvoice(&mailbox.Message{DTE: []byte(ccf)})
	facturaInvoice, _ := toInvoice(&mailbox.Message{DTE: []byte(factura)})
	credit := ccfInvoice
	credit.TipoDte = "05"
	credit.CodigoGeneracion = "NC"
	credit.Gravado, credit.IVA, credit.Total = 10, 1.3, 11.3
	return []store.Invoice{ccfInvoice, facturaInvoice, credit}
}

func TestPurchasesBook(t *testing.T) {
	content, err := purchasesBook(sample(), "Del 2026-11-01 al 2026-11-30")
	if err != nil {
		t.Fatal(err)
	}
	f, err := excelize.OpenReader(bytes.NewReader(content))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()

	if sheets := f.GetSheetList(); len(sheets) != 2 || sheets[0] != "Libro de compras" || sheets[1] != "Otros documentos" {
		t.Fatalf("hojas inesperadas: %v", sheets)
	}

	cell := func(sheet, ref string) string {
		value, _ := f.GetCellValue(sheet, ref)
		return value
	}
	book := "Libro de compras"
	if title := cell(book, "A1"); title != "LIBRO DE COMPRAS — Cliente de Prueba, S.A. de C.V. (NRC 654321)" {
		t.Errorf("título: %q", title)
	}
	// Fila 6: el crédito fiscal. Fila 7: la nota de crédito, en negativo.
	if got := cell(book, "H6"); got != "Proveedor de Prueba, S.A. de C.V." {
		t.Errorf("proveedor: %q", got)
	}
	if got := cell(book, "L6"); got != "13.00" {
		t.Errorf("crédito fiscal: %q", got)
	}
	if got := cell(book, "L7"); got != "-1.30" {
		t.Errorf("la nota de crédito debe restar: %q", got)
	}
	if formula, _ := f.GetCellFormula(book, "L8"); formula != "SUM(L6:L7)" {
		t.Errorf("fórmula de totales: %q", formula)
	}
	// Si no hay documentos con crédito fiscal, la hoja lo explica.
	onlyFactura, _ := purchasesBook(sample()[1:2], "")
	g, _ := excelize.OpenReader(bytes.NewReader(onlyFactura))
	if note, _ := g.GetCellValue(book, "A4"); !strings.Contains(note, "Los 1 documentos recibidos") {
		t.Errorf("nota de libro vacío: %q", note)
	}
	_ = g.Close()

	// La factura de consumidor final no da crédito fiscal: va a la otra hoja.
	if got := cell("Otros documentos", "C6"); got != "Factura" {
		t.Errorf("otros documentos: %q", got)
	}
}

func TestDetailBook(t *testing.T) {
	content, err := detailBook(sample(), "Todas las fechas")
	if err != nil {
		t.Fatal(err)
	}
	f, err := excelize.OpenReader(bytes.NewReader(content))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()

	rows, _ := f.GetRows("Facturas")
	// 3 filas de encabezado + 1 vacía + títulos + 3 facturas + totales
	if len(rows) != 9 {
		t.Errorf("filas: %d", len(rows))
	}

	// Un libro sin facturas también debe generarse.
	if _, err := detailBook(nil, ""); err != nil {
		t.Errorf("libro vacío: %v", err)
	}
}
