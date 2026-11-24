package dte

import "encoding/json"

type DocumentoElectronico struct {
	Identificacion Identificacion  `json:"identificacion"`
	Emisor         Emisor          `json:"emisor"`
	Receptor       json.RawMessage `json:"receptor"`
	Cuerpo         json.RawMessage `json:"cuerpoDocumento"`
	Resumen        json.RawMessage `json:"resumen"`
	SelloRecibido  string          `json:"selloRecibido"`
}

type Identificacion struct {
	Version          int    `json:"version"`
	Ambiente         string `json:"ambiente"`
	TipoDte          string `json:"tipoDte"`
	NumeroControl    string `json:"numeroControl"`
	CodigoGeneracion string `json:"codigoGeneracion"`
	FecEmi           string `json:"fecEmi"`
	HorEmi           string `json:"horEmi"`
	TipoMoneda       string `json:"tipoMoneda"`
}

type Emisor struct {
	NIT    string `json:"nit"`
	NRC    string `json:"nrc"`
	Nombre string `json:"nombre"`
}

func Parse(content []byte) (*DocumentoElectronico, error) {
	var doc DocumentoElectronico
	if err := json.Unmarshal(content, &doc); err != nil {
		return nil, err
	}
	return &doc, nil
}
