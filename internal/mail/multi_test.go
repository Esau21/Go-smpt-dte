package mail

import (
	"net/http"
	"net/http/httptest"
	"testing"

	mail_outlook "github.com/Esau21/Go-smpt-dte/internal/mail/outlook"
	"github.com/Esau21/Go-smpt-dte/internal/mailbox"
	"github.com/gin-gonic/gin"
)

// fake es un proveedor de mentira: tiene sesión si la petición trae su cookie.
type fake struct {
	IMail // lo que la prueba no usa queda sin implementar
	name  string
	calls []string
}

func (f *fake) HasSession(c *gin.Context) bool {
	_, err := c.Cookie(f.name)
	return err == nil
}

func (f *fake) WithSession(next gin.HandlerFunc) gin.HandlerFunc { return next }
func (f *fake) Login(c *gin.Context)                             { c.String(200, "login "+f.name) }
func (f *fake) Callback(c *gin.Context)                          { c.String(200, "callback "+f.name) }
func (f *fake) Me(c *gin.Context)                                { c.String(200, "me "+f.name) }
func (f *fake) ErrorStatus(error) int                            { return 502 }

func (f *fake) Logout(c *gin.Context) {
	f.calls = append(f.calls, "logout")
	c.SetCookie(f.name, "", -1, "/", "", false, true)
	c.Status(http.StatusNoContent)
}

func (f *fake) Account(*gin.Context) (mailbox.Account, bool) {
	return mailbox.Account{Provider: f.name, Email: f.name + "@example.com"}, true
}

func TestMulti(t *testing.T) {
	gmail, outlook := &fake{name: "gmail"}, &fake{name: "outlook"}
	m := &multi{
		names:     []string{"gmail", "outlook"},
		providers: map[string]provider{"gmail": gmail, "outlook": outlook},
	}

	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.GET("/auth", m.Login)
	r.GET("/api/auth/google/callback", m.Callback)
	r.GET(mail_outlook.CallbackPath, m.Callback)
	r.DELETE("/session", m.Logout)
	r.GET("/me", m.WithSession(m.Me))
	r.GET("/account", m.WithSession(func(c *gin.Context) {
		account, _ := m.Account(c)
		c.String(200, account.Email)
	}))

	call := func(method, target, cookie string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, target, nil)
		if cookie != "" {
			req.AddCookie(&http.Cookie{Name: cookie, Value: "1"})
		}
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		return w
	}

	checks := []struct {
		name, method, target, cookie string
		status                       int
		body                         string
	}{
		{"login por defecto", "GET", "/auth", "", 200, "login gmail"},
		{"login con Outlook", "GET", "/auth?provider=outlook", "", 200, "login outlook"},
		{"proveedor desconocido", "GET", "/auth?provider=yahoo", "", 400, ""},
		{"regreso de Google", "GET", "/api/auth/google/callback", "", 200, "callback gmail"},
		{"regreso de Microsoft", "GET", mail_outlook.CallbackPath, "", 200, "callback outlook"},
		{"sin sesión", "GET", "/me", "", 401, ""},
		{"sesión de Gmail", "GET", "/me", "gmail", 200, "me gmail"},
		{"sesión de Outlook", "GET", "/me", "outlook", 200, "me outlook"},
		{"la cuenta sale del proveedor activo", "GET", "/account", "outlook", 200, "outlook@example.com"},
	}
	for _, c := range checks {
		w := call(c.method, c.target, c.cookie)
		if w.Code != c.status || (c.body != "" && w.Body.String() != c.body) {
			t.Errorf("%s: %d %q", c.name, w.Code, w.Body.String())
		}
	}

	// Entrar con Outlook cierra la sesión que había con Gmail.
	if w := call("GET", mail_outlook.CallbackPath, "gmail"); w.Body.String() != "callback outlook" || len(gmail.calls) != 1 {
		t.Errorf("al cambiar de proveedor debe cerrarse el anterior: %q %v", w.Body.String(), gmail.calls)
	}

	// Cerrar sesión avisa a todos y responde una sola vez.
	gmail.calls, outlook.calls = nil, nil
	if w := call("DELETE", "/session", "gmail"); w.Code != http.StatusNoContent || len(gmail.calls)+len(outlook.calls) != 2 || len(w.Header().Values("Set-Cookie")) != 2 {
		t.Errorf("cerrar sesión: %d, llamadas %v %v, cookies %v", w.Code, gmail.calls, outlook.calls, w.Header().Values("Set-Cookie"))
	}
}
