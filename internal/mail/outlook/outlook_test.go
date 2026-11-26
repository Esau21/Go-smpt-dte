package mail_outlook

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"golang.org/x/oauth2"
)

func TestToKQL(t *testing.T) {
	now := time.Date(2026, 11, 26, 12, 0, 0, 0, time.UTC)
	cases := map[string]string{
		"":                                     "",
		"filename:json":                        "attachment:json",
		"siman filename:json filename:pdf":     "siman attachment:json attachment:pdf",
		"from:(almacenes siman) filename:json": "from:almacenes from:siman attachment:json",
		"subject:(factura) after:2026/11/01":   "subject:factura received>=2026-11-01",
		"before:2026/12/01 newer_than:7d":      "received<2026-12-01 received>=2026-11-19",
		`"comillas" from:("x")`:                "comillas from:x",
		"has:attachment label:inbox":           "hasattachment:true inbox",
	}
	for in, want := range cases {
		if got := toKQL(in, now); got != want {
			t.Errorf("toKQL(%q)\n obtuve  %q\n esperaba %q", in, got, want)
		}
	}
}

func TestListURL(t *testing.T) {
	plain, _ := url.Parse(listURL("", 15))
	if !strings.HasSuffix(plain.Path, "/me/mailFolders/inbox/messages") || plain.Query().Get("$orderby") == "" || plain.Query().Get("$top") != "15" {
		t.Errorf("sin búsqueda: %s", plain)
	}
	search, _ := url.Parse(listURL("attachment:json", 20))
	if !strings.HasSuffix(search.Path, "/me/messages") || search.Query().Get("$search") != `"attachment:json"` || search.Query().Has("$orderby") {
		t.Errorf("con búsqueda: %s", search)
	}
}

func TestPageToken(t *testing.T) {
	next := graph + "/me/messages?$skiptoken=abc"
	if got, ok := decodePage(encodePage(next)); !ok || got != next {
		t.Errorf("ida y vuelta: %q %v", got, ok)
	}
	// Una página que apunte fuera de Graph se rechaza: la API no debe
	// mandar el token del usuario a otra dirección.
	for _, bad := range []string{encodePage("https://example.com/robar"), "no-es-base64!", encodePage("https://graph.microsoft.com.evil.example/x")} {
		if _, ok := decodePage(bad); ok {
			t.Errorf("debe rechazarse: %q", bad)
		}
	}
	if encodePage("") != "" {
		t.Error("sin página siguiente no hay token")
	}
}

func TestErrorStatus(t *testing.T) {
	oc := &Client{}
	cases := []struct {
		err  error
		want int
	}{
		{errNoSession, 401},
		{&graphError{status: 404}, 404},
		{&graphError{status: 429}, 429},
		{&graphError{status: 500}, 502},
		{&oauth2.RetrieveError{ErrorCode: "invalid_grant"}, 401},
		{context.DeadlineExceeded, 504},
	}
	for _, c := range cases {
		if got := oc.ErrorStatus(c.err); got != c.want {
			t.Errorf("ErrorStatus(%v) = %d, esperaba %d", c.err, got, c.want)
		}
	}
}

// TestSessionRequests comprueba, contra un servidor de mentira, cómo se
// leen las respuestas de Graph.
func TestSessionRequests(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Prefer") != `IdType="ImmutableId"` {
			t.Errorf("falta pedir identificadores estables: %q", r.Header.Get("Prefer"))
		}
		switch r.URL.Path {
		case "/lista":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"@odata.nextLink": graph + "/siguiente",
				"value": []map[string]any{{
					"id": "AAA=", "conversationId": "c1", "subject": "Su factura", "bodyPreview": "Adjuntamos",
					"isRead": false, "receivedDateTime": "2026-11-20T10:00:00Z",
					"from": map[string]any{"emailAddress": map[string]any{"name": "Ventas", "address": "v@example.com"}},
				}},
			})
		case "/prohibido":
			w.WriteHeader(http.StatusForbidden)
		default:
			_, _ = w.Write([]byte("contenido"))
		}
	}))
	defer server.Close()

	s := session{http: server.Client()}
	ctx := context.Background()

	var page graphPage
	if err := s.get(ctx, server.URL+"/lista", &page); err != nil {
		t.Fatal(err)
	}
	if len(page.Value) != 1 || page.Value[0].from() != "Ventas <v@example.com>" || page.Value[0].IsRead || page.NextLink == "" {
		t.Errorf("página mal leída: %+v", page)
	}

	if content, err := s.raw(ctx, server.URL+"/archivo"); err != nil || string(content) != "contenido" {
		t.Errorf("descarga: %q %v", content, err)
	}

	err := s.get(ctx, server.URL+"/prohibido", &page)
	if status := (&Client{}).ErrorStatus(err); status != 403 {
		t.Errorf("un 403 de Graph debe llegar como 403: %v -> %d", err, status)
	}
}
