package dte

import "encoding/json"

// Amounts son los montos de un DTE ya normalizados, sin importar el tipo
// de documento del que salieron.
type Amounts struct {
	NoSujeto     float64
	Exento       float64
	Gravado      float64
	IVA          float64
	IVAPercibido float64
	IVARetenido  float64
	Total        float64
}

// Party es el receptor del documento.
type Party struct {
	Documento string
	NRC       string
	Nombre    string
}

type resumen struct {
	TotalNoSuj          float64 `json:"totalNoSuj"`
	TotalExenta         float64 `json:"totalExenta"`
	TotalGravada        float64 `json:"totalGravada"`
	TotalIva            float64 `json:"totalIva"`
	IvaPerci1           float64 `json:"ivaPerci1"`
	IvaRete1            float64 `json:"ivaRete1"`
	MontoTotalOperacion float64 `json:"montoTotalOperacion"`
	TotalPagar          float64 `json:"totalPagar"`
	TotalCompra         float64 `json:"totalCompra"`
	Tributos            []struct {
		Codigo string  `json:"codigo"`
		Valor  float64 `json:"valor"`
	} `json:"tributos"`
}

// codigoIVA es el código del tributo "Impuesto al Valor Agregado 13%".
const codigoIVA = "20"

// Amounts lee los montos del resumen. Si el resumen no se puede leer,
// devuelve ceros: el documento se guarda igual y se revisa a mano.
func (d *DocumentoElectronico) Amounts() Amounts {
	var r resumen
	if len(d.Resumen) > 0 {
		_ = json.Unmarshal(d.Resumen, &r)
	}

	// En el crédito fiscal el IVA viene como tributo; en la factura de
	// consumidor final viene incluido en el precio y se informa en totalIva.
	iva := 0.0
	for _, t := range r.Tributos {
		if t.Codigo == codigoIVA {
			iva += t.Valor
		}
	}
	if iva == 0 {
		iva = r.TotalIva
	}

	total := r.TotalPagar
	if total == 0 {
		total = r.MontoTotalOperacion
	}
	if total == 0 {
		total = r.TotalCompra
	}

	return Amounts{
		NoSujeto:     r.TotalNoSuj,
		Exento:       r.TotalExenta,
		Gravado:      r.TotalGravada,
		IVA:          iva,
		IVAPercibido: r.IvaPerci1,
		IVARetenido:  r.IvaRete1,
		Total:        total,
	}
}

// ReceptorParty lee los datos del receptor.
func (d *DocumentoElectronico) ReceptorParty() Party {
	var r struct {
		NIT          string `json:"nit"`
		NumDocumento string `json:"numDocumento"`
		NRC          string `json:"nrc"`
		Nombre       string `json:"nombre"`
	}
	if len(d.Receptor) > 0 {
		_ = json.Unmarshal(d.Receptor, &r)
	}
	doc := r.NIT
	if doc == "" {
		doc = r.NumDocumento
	}
	return Party{Documento: doc, NRC: r.NRC, Nombre: r.Nombre}
}

// selloFallback busca el sello de recepción en los otros lugares donde
// algunos emisores lo colocan dentro del JSON.
func selloFallback(content []byte) string {
	var alt struct {
		RespuestaHacienda struct {
			SelloRecibido string `json:"selloRecibido"`
		} `json:"respuestaHacienda"`
		ResponseMH struct {
			SelloRecibido string `json:"selloRecibido"`
		} `json:"responseMH"`
		Sello string `json:"sello"`
	}
	_ = json.Unmarshal(content, &alt)
	switch {
	case alt.RespuestaHacienda.SelloRecibido != "":
		return alt.RespuestaHacienda.SelloRecibido
	case alt.ResponseMH.SelloRecibido != "":
		return alt.ResponseMH.SelloRecibido
	}
	return alt.Sello
}
