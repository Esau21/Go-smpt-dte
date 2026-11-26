package invoices

import (
	"bytes"
	"fmt"

	"github.com/Esau21/Go-smpt-dte/internal/store"
	"github.com/xuri/excelize/v2"
)

var tipos = map[string]string{
	"01": "Factura",
	"03": "Comprobante de crédito fiscal",
	"04": "Nota de remisión",
	"05": "Nota de crédito",
	"06": "Nota de débito",
	"07": "Comprobante de retención",
	"08": "Comprobante de liquidación",
	"09": "Documento contable de liquidación",
	"11": "Factura de exportación",
	"14": "Factura de sujeto excluido",
	"15": "Comprobante de donación",
}

func tipoName(code string) string {
	if name, ok := tipos[code]; ok {
		return name
	}
	return "Tipo " + code
}

// column describe una columna de una hoja: su título, su ancho, si es un
// monto (se suma al final y lleva formato de dinero) y cómo sacar su valor.
type column struct {
	title string
	width float64
	money bool
	value func(n int, i store.Invoice) any
}

// signed hace que las notas de crédito aparezcan en negativo.
func signed(i store.Invoice, amount float64) float64 {
	if i.TipoDte == "05" {
		return -amount
	}
	return amount
}

// sheet escribe una hoja con título, periodo, encabezados, las filas y una
// fila de totales con fórmulas, para que el contador pueda editarla.
func sheet(f *excelize.File, name, title, period, note string, cols []column, items []store.Invoice) error {
	if _, err := f.NewSheet(name); err != nil {
		return err
	}

	bold, _ := f.NewStyle(&excelize.Style{Font: &excelize.Font{Bold: true, Size: 14}})
	muted, _ := f.NewStyle(&excelize.Style{Font: &excelize.Font{Color: "6B6B76"}})
	head, _ := f.NewStyle(&excelize.Style{
		Font:      &excelize.Font{Bold: true},
		Fill:      excelize.Fill{Type: "pattern", Pattern: 1, Color: []string{"E9F9B8"}},
		Alignment: &excelize.Alignment{Vertical: "center", WrapText: true},
		Border:    []excelize.Border{{Type: "bottom", Color: "111111", Style: 1}},
	})
	moneyFmt := `#,##0.00;[Red]-#,##0.00`
	money, _ := f.NewStyle(&excelize.Style{CustomNumFmt: &moneyFmt})
	total, _ := f.NewStyle(&excelize.Style{
		Font:         &excelize.Font{Bold: true},
		CustomNumFmt: &moneyFmt,
		Border:       []excelize.Border{{Type: "top", Color: "111111", Style: 1}},
	})

	_ = f.SetCellValue(name, "A1", title)
	_ = f.SetCellStyle(name, "A1", "A1", bold)
	_ = f.SetCellValue(name, "A2", period)
	_ = f.SetCellValue(name, "A3", "Generado desde los DTE recibidos por correo. Revisar antes de presentar.")
	_ = f.SetCellStyle(name, "A2", "A3", muted)
	if note != "" {
		warn, _ := f.NewStyle(&excelize.Style{Font: &excelize.Font{Bold: true, Color: "9B5631"}})
		_ = f.SetCellValue(name, "A4", note)
		_ = f.SetCellStyle(name, "A4", "A4", warn)
	}

	const headRow = 5
	for c, col := range cols {
		letter, _ := excelize.ColumnNumberToName(c + 1)
		cell := fmt.Sprintf("%s%d", letter, headRow)
		_ = f.SetCellValue(name, cell, col.title)
		_ = f.SetColWidth(name, letter, letter, col.width)
		if col.money {
			_ = f.SetColStyle(name, letter, money)
		}
	}
	lastLetter, _ := excelize.ColumnNumberToName(len(cols))
	_ = f.SetCellStyle(name, fmt.Sprintf("A%d", headRow), fmt.Sprintf("%s%d", lastLetter, headRow), head)
	_ = f.SetRowHeight(name, headRow, 32)

	for r, item := range items {
		row := headRow + 1 + r
		for c, col := range cols {
			letter, _ := excelize.ColumnNumberToName(c + 1)
			_ = f.SetCellValue(name, fmt.Sprintf("%s%d", letter, row), col.value(r+1, item))
		}
	}

	// Fila de totales
	first, last := headRow+1, headRow+len(items)
	totalRow := last + 1
	_ = f.SetCellStyle(name, fmt.Sprintf("A%d", totalRow), fmt.Sprintf("%s%d", lastLetter, totalRow), total)
	labeled := false
	for c, col := range cols {
		letter, _ := excelize.ColumnNumberToName(c + 1)
		cell := fmt.Sprintf("%s%d", letter, totalRow)
		switch {
		case col.money && len(items) > 0:
			_ = f.SetCellFormula(name, cell, fmt.Sprintf("SUM(%s%d:%s%d)", letter, first, letter, last))
		case col.money:
			_ = f.SetCellValue(name, cell, 0)
		case !labeled && c > 0:
			_ = f.SetCellValue(name, cell, "TOTALES")
			labeled = true
		}
	}

	// Encabezados fijos al desplazarse
	return f.SetPanes(name, &excelize.Panes{
		Freeze:      true,
		YSplit:      headRow,
		TopLeftCell: fmt.Sprintf("A%d", headRow+1),
		ActivePane:  "bottomLeft",
	})
}

func finish(f *excelize.File) ([]byte, error) {
	// Quita la hoja vacía con la que nace el archivo
	_ = f.DeleteSheet("Sheet1")
	f.SetActiveSheet(0)

	var buf bytes.Buffer
	if err := f.Write(&buf); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

var purchaseColumns = []column{
	{"N°", 6, false, func(n int, _ store.Invoice) any { return n }},
	{"Fecha de emisión", 13, false, func(_ int, i store.Invoice) any { return i.Fecha }},
	{"Tipo de documento", 28, false, func(_ int, i store.Invoice) any { return tipoName(i.TipoDte) }},
	{"Número de control", 34, false, func(_ int, i store.Invoice) any { return i.NumeroControl }},
	{"Código de generación", 40, false, func(_ int, i store.Invoice) any { return i.CodigoGeneracion }},
	{"NRC del proveedor", 14, false, func(_ int, i store.Invoice) any { return i.EmisorNRC }},
	{"NIT del proveedor", 18, false, func(_ int, i store.Invoice) any { return i.EmisorNIT }},
	{"Nombre del proveedor", 42, false, func(_ int, i store.Invoice) any { return i.EmisorNombre }},
	{"Compras exentas", 14, true, func(_ int, i store.Invoice) any { return signed(i, i.Exento) }},
	{"Compras no sujetas", 14, true, func(_ int, i store.Invoice) any { return signed(i, i.NoSujeto) }},
	{"Compras gravadas", 14, true, func(_ int, i store.Invoice) any { return signed(i, i.Gravado) }},
	{"Crédito fiscal (IVA)", 14, true, func(_ int, i store.Invoice) any { return signed(i, i.IVA) }},
	{"IVA percibido", 13, true, func(_ int, i store.Invoice) any { return signed(i, i.IVAPercibido) }},
	{"IVA retenido", 13, true, func(_ int, i store.Invoice) any { return signed(i, i.IVARetenido) }},
	{"Total compras", 14, true, func(_ int, i store.Invoice) any { return signed(i, i.Total) }},
}

// Documentos que dan derecho a crédito fiscal y van al libro de compras:
// comprobante de crédito fiscal, nota de crédito y nota de débito.
func givesCredit(tipo string) bool {
	return tipo == "03" || tipo == "05" || tipo == "06"
}

// purchasesBook genera el libro de compras: una hoja con los documentos que
// dan crédito fiscal y otra con el resto (facturas de consumidor final,
// sujetos excluidos, etc.), que no entran al libro pero conviene tener a mano.
func purchasesBook(items []store.Invoice, period string) ([]byte, error) {
	var credit, other []store.Invoice
	for _, i := range items {
		if givesCredit(i.TipoDte) {
			credit = append(credit, i)
		} else {
			other = append(other, i)
		}
	}

	f := excelize.NewFile()
	defer f.Close()

	title := "LIBRO DE COMPRAS"
	if name, nrc := mainReceptor(credit); name != "" {
		title += " — " + name
		if nrc != "" {
			title += " (NRC " + nrc + ")"
		}
	}
	// Un libro vacío parece un error: se explica por qué y dónde está lo demás.
	note := ""
	if len(credit) == 0 {
		note = "No hay comprobantes de crédito fiscal, notas de crédito ni notas de débito en este periodo."
		if len(other) > 0 {
			note += fmt.Sprintf(" Los %d documentos recibidos (facturas de consumidor final y otros) están en la hoja «Otros documentos».", len(other))
		}
	}
	if err := sheet(f, "Libro de compras", title, period, note, purchaseColumns, credit); err != nil {
		return nil, err
	}
	otherNote := "Estos documentos no dan derecho a crédito fiscal, por eso no forman parte del libro de compras."
	if err := sheet(f, "Otros documentos", "DOCUMENTOS SIN CRÉDITO FISCAL", period, otherNote, purchaseColumns, other); err != nil {
		return nil, err
	}
	return finish(f)
}

// mainReceptor devuelve el receptor que más se repite: el contribuyente
// dueño del libro.
func mainReceptor(items []store.Invoice) (name, nrc string) {
	count := map[string]int{}
	best := 0
	for _, i := range items {
		if i.ReceptorNombre == "" {
			continue
		}
		count[i.ReceptorNombre]++
		if count[i.ReceptorNombre] > best {
			best = count[i.ReceptorNombre]
			name, nrc = i.ReceptorNombre, i.ReceptorNRC
		}
	}
	return name, nrc
}

var detailColumns = []column{
	{"N°", 6, false, func(n int, _ store.Invoice) any { return n }},
	{"Fecha", 12, false, func(_ int, i store.Invoice) any { return i.Fecha }},
	{"Hora", 10, false, func(_ int, i store.Invoice) any { return i.Hora }},
	{"Tipo de documento", 28, false, func(_ int, i store.Invoice) any { return tipoName(i.TipoDte) }},
	{"Emisor", 42, false, func(_ int, i store.Invoice) any { return i.EmisorNombre }},
	{"NIT emisor", 18, false, func(_ int, i store.Invoice) any { return i.EmisorNIT }},
	{"NRC emisor", 12, false, func(_ int, i store.Invoice) any { return i.EmisorNRC }},
	{"Receptor", 36, false, func(_ int, i store.Invoice) any { return i.ReceptorNombre }},
	{"Documento receptor", 18, false, func(_ int, i store.Invoice) any { return i.ReceptorDoc }},
	{"Número de control", 34, false, func(_ int, i store.Invoice) any { return i.NumeroControl }},
	{"Código de generación", 40, false, func(_ int, i store.Invoice) any { return i.CodigoGeneracion }},
	{"Sello de recepción", 44, false, func(_ int, i store.Invoice) any { return i.Sello }},
	{"Exento", 12, true, func(_ int, i store.Invoice) any { return signed(i, i.Exento) }},
	{"No sujeto", 12, true, func(_ int, i store.Invoice) any { return signed(i, i.NoSujeto) }},
	{"Gravado", 12, true, func(_ int, i store.Invoice) any { return signed(i, i.Gravado) }},
	{"IVA", 12, true, func(_ int, i store.Invoice) any { return signed(i, i.IVA) }},
	{"Total", 13, true, func(_ int, i store.Invoice) any { return signed(i, i.Total) }},
	{"Moneda", 9, false, func(_ int, i store.Invoice) any { return i.Moneda }},
	{"Estado", 12, false, func(_ int, i store.Invoice) any { return i.Status }},
	{"Categoría", 20, false, func(_ int, i store.Invoice) any { return i.Category }},
	{"Notas", 40, false, func(_ int, i store.Invoice) any { return i.Notes }},
}

// detailBook genera un Excel con todas las facturas y todos sus datos.
func detailBook(items []store.Invoice, period string) ([]byte, error) {
	f := excelize.NewFile()
	defer f.Close()

	if err := sheet(f, "Facturas", "FACTURAS RECIBIDAS", period, "", detailColumns, items); err != nil {
		return nil, err
	}
	return finish(f)
}
