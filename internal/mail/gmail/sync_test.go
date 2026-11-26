package mail_gmail

import (
	"encoding/base64"
	"testing"

	"google.golang.org/api/gmail/v1"
)

func TestFindJSONPart(t *testing.T) {
	// Estructura típica de un correo de facturación: el cuerpo en texto y
	// HTML, y los adjuntos (PDF y JSON) anidados en otra parte.
	message := &gmail.MessagePart{
		MimeType: "multipart/mixed",
		Parts: []*gmail.MessagePart{
			{MimeType: "multipart/alternative", Parts: []*gmail.MessagePart{
				{MimeType: "text/plain", Body: &gmail.MessagePartBody{Size: 10}},
				{MimeType: "text/html", Body: &gmail.MessagePartBody{Size: 20}},
			}},
			{Filename: "factura.pdf", Body: &gmail.MessagePartBody{AttachmentId: "pdf", Size: 900000}},
			{MimeType: "multipart/related", Parts: []*gmail.MessagePart{
				{Filename: "DTE.JSON", Body: &gmail.MessagePartBody{AttachmentId: "json", Size: 6000}},
			}},
		},
	}
	part := findJSONPart(message)
	if part == nil || part.Body.AttachmentId != "json" {
		t.Fatalf("debe encontrar el .json anidado, sin importar mayúsculas: %+v", part)
	}

	if findJSONPart(&gmail.MessagePart{Filename: "solo.pdf", Body: &gmail.MessagePartBody{}}) != nil {
		t.Error("un correo sin .json no tiene DTE")
	}
	if findJSONPart(nil) != nil {
		t.Error("un correo sin contenido no tiene DTE")
	}
}

func TestDecodeBody(t *testing.T) {
	want := `{"identificacion":{"codigoGeneracion":"ñ?>"}}`
	// Gmail usa base64 para URL; puede venir con relleno o sin él.
	for _, encoded := range []string{
		base64.URLEncoding.EncodeToString([]byte(want)),
		base64.RawURLEncoding.EncodeToString([]byte(want)),
	} {
		got, err := decodeBody(encoded)
		if err != nil || string(got) != want {
			t.Errorf("decodeBody(%q) = %q, %v", encoded, got, err)
		}
	}
}

func TestHeaderOf(t *testing.T) {
	m := &gmail.Message{Payload: &gmail.MessagePart{Headers: []*gmail.MessagePartHeader{
		{Name: "subject", Value: "=?UTF-8?B?RmFjdHVyYSBlbGVjdHLDs25pY2E=?="},
		{Name: "From", Value: "ventas@example.com"},
	}}}
	if got := headerOf(m, "Subject"); got != "Factura electrónica" {
		t.Errorf("asunto: %q", got)
	}
	if got := headerOf(m, "from"); got != "ventas@example.com" {
		t.Errorf("remitente: %q", got)
	}
	if got := headerOf(&gmail.Message{}, "Subject"); got != "" {
		t.Errorf("sin cabeceras: %q", got)
	}
}
