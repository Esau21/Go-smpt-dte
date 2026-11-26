package dte

import (
	"bytes"
	"encoding/json"
	"unicode/utf8"

	"golang.org/x/text/encoding/charmap"
)

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

// normalize deja el contenido del adjunto como UTF-8 limpio. Algunos
// emisores generan el .json en Windows-1252 (la Ñ y las tildes llegarían
// como "�") o le anteponen una marca BOM que el lector de JSON rechaza.
func normalize(content []byte) []byte {
	content = bytes.TrimPrefix(content, []byte{0xEF, 0xBB, 0xBF})

	// Paso 1: los bytes sueltos que no son UTF-8 se leen como Windows-1252.
	// Se hace byte a byte porque hay archivos que mezclan las dos formas.
	if !utf8.Valid(content) {
		fixed := make([]byte, 0, len(content)+16)
		for len(content) > 0 {
			r, size := utf8.DecodeRune(content)
			if r == utf8.RuneError && size == 1 {
				fixed = utf8.AppendRune(fixed, charmap.Windows1252.DecodeByte(content[0]))
			} else {
				fixed = append(fixed, content[:size]...)
			}
			content = content[size:]
		}
		content = fixed
	}

	// Paso 2: texto que el emisor ya mandó dañado, con cada letra acentuada
	// convertida en dos ("COMPAÃ‘IA" en vez de "COMPAÑIA"). Se reconstruye la
	// letra original a partir de ese par.
	if !bytes.ContainsAny(content, "ÃÂ") {
		return content
	}
	runes := []rune(string(content))
	out := make([]byte, 0, len(content))
	for i := 0; i < len(runes); i++ {
		r := runes[i]
		if (r == 'Ã' || r == 'Â') && i+1 < len(runes) {
			if second, ok := continuationByte(runes[i+1]); ok {
				if letter, _ := utf8.DecodeRune([]byte{byte(r), second}); letter != utf8.RuneError {
					out = utf8.AppendRune(out, letter)
					i++
					continue
				}
			}
		}
		out = utf8.AppendRune(out, r)
	}
	return out
}

// continuationByte devuelve el byte (0x80–0xBF) que, leído como
// Windows-1252, se ve como el carácter r.
func continuationByte(r rune) (byte, bool) {
	if r >= 0x80 && r <= 0xBF {
		return byte(r), true
	}
	if b, ok := charmap.Windows1252.EncodeRune(r); ok && b >= 0x80 && b <= 0xBF {
		return b, true
	}
	return 0, false
}

func Parse(content []byte) (*DocumentoElectronico, error) {
	content = normalize(content)
	var doc DocumentoElectronico
	if err := json.Unmarshal(content, &doc); err != nil {
		return nil, err
	}
	if doc.SelloRecibido == "" {
		doc.SelloRecibido = selloFallback(content)
	}
	return &doc, nil
}
