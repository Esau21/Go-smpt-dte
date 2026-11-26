package mail

import (
	"net/http"
	"strings"

	mail_outlook "github.com/Esau21/Go-smpt-dte/internal/mail/outlook"
	"github.com/Esau21/Go-smpt-dte/internal/mailbox"
	"github.com/Esau21/Go-smpt-dte/internal/validations"
	"github.com/gin-gonic/gin"
)

// provider es un proveedor de correo que además sabe decir si la petición
// trae una sesión suya.
type provider interface {
	IMail
	HasSession(c *gin.Context) bool
}

// multi reparte cada petición al proveedor con el que el usuario inició
// sesión. Solo se usa cuando hay más de un proveedor configurado; con uno
// solo, las rutas hablan con él directamente.
type multi struct {
	names     []string // en el orden de MAIL_PROVIDER; el primero es el predeterminado
	providers map[string]provider
}

const activeKey = "internal/mail.provider"

func (m *multi) Providers() []string { return m.names }

// Login inicia sesión con el proveedor pedido en ?provider=.
func (m *multi) Login(c *gin.Context) {
	name := c.DefaultQuery("provider", m.names[0])
	p, ok := m.providers[name]
	if !ok {
		validations.Fail(c, 400, "unknown_provider")
		return
	}
	p.Login(c)
}

// Callback recibe el regreso del login; cada proveedor tiene su propia ruta.
func (m *multi) Callback(c *gin.Context) {
	name := "gmail"
	if strings.HasSuffix(c.FullPath(), mail_outlook.CallbackPath) {
		name = "outlook"
	}
	p, ok := m.providers[name]
	if !ok {
		validations.Fail(c, 404, "unknown_provider")
		return
	}
	// Una sola cuenta a la vez: al entrar con un proveedor se cierra la
	// sesión que hubiera con otro.
	for other, q := range m.providers {
		if other != name && q.HasSession(c) {
			q.Logout(&gin.Context{Request: c.Request, Writer: discard{c.Writer}})
		}
	}
	p.Callback(c)
}

// discard deja pasar las cabeceras (las cookies que se borran) pero ignora
// el código de respuesta de un cierre de sesión hecho "por debajo".
type discard struct{ gin.ResponseWriter }

func (discard) WriteHeader(int)           {}
func (discard) WriteHeaderNow()           {}
func (discard) Write([]byte) (int, error) { return 0, nil }

func (m *multi) Logout(c *gin.Context) {
	for _, p := range m.providers {
		p.Logout(&gin.Context{Request: c.Request, Writer: discard{c.Writer}})
	}
	c.Status(http.StatusNoContent)
}

// WithSession busca qué proveedor tiene sesión y le pasa la petición.
func (m *multi) WithSession(next gin.HandlerFunc) gin.HandlerFunc {
	return func(c *gin.Context) {
		for _, name := range m.names {
			if p := m.providers[name]; p.HasSession(c) {
				c.Set(activeKey, p)
				p.WithSession(next)(c)
				return
			}
		}
		validations.Fail(c, 401, "login_required")
	}
}

// active devuelve el proveedor de esta petición, o responde 401.
func (m *multi) active(c *gin.Context) (provider, bool) {
	value, _ := c.Get(activeKey)
	p, ok := value.(provider)
	if !ok {
		c.Abort()
		validations.Fail(c, 401, "login_required")
	}
	return p, ok
}

func (m *multi) Me(c *gin.Context) {
	if p, ok := m.active(c); ok {
		p.Me(c)
	}
}

func (m *multi) ListMessages(c *gin.Context) {
	if p, ok := m.active(c); ok {
		p.ListMessages(c)
	}
}

func (m *multi) GetMessage(c *gin.Context) {
	if p, ok := m.active(c); ok {
		p.GetMessage(c)
	}
}

func (m *multi) GetDTE(c *gin.Context) {
	if p, ok := m.active(c); ok {
		p.GetDTE(c)
	}
}

func (m *multi) GetAttachment(c *gin.Context) {
	if p, ok := m.active(c); ok {
		p.GetAttachment(c)
	}
}

// Lo que sigue lo usa el módulo de facturas, que ya comprobó la sesión.

func (m *multi) quiet(c *gin.Context) provider {
	value, _ := c.Get(activeKey)
	p, _ := value.(provider)
	return p
}

func (m *multi) Account(c *gin.Context) (mailbox.Account, bool) {
	if p := m.quiet(c); p != nil {
		return p.Account(c)
	}
	return mailbox.Account{}, false
}

func (m *multi) ListDTEMessages(c *gin.Context, pageToken string, limit int) ([]string, string, error) {
	if p := m.quiet(c); p != nil {
		return p.ListDTEMessages(c, pageToken, limit)
	}
	return nil, "", errNoProvider
}

func (m *multi) FetchDTE(c *gin.Context, id string) (*mailbox.Message, error) {
	if p := m.quiet(c); p != nil {
		return p.FetchDTE(c, id)
	}
	return nil, errNoProvider
}

func (m *multi) FetchAttachment(c *gin.Context, id string, index int) (*mailbox.Attachment, error) {
	if p := m.quiet(c); p != nil {
		return p.FetchAttachment(c, id, index)
	}
	return nil, errNoProvider
}

// ErrorStatus no recibe la petición, así que pregunta a cada proveedor y se
// queda con la primera respuesta que no sea el genérico 502.
func (m *multi) ErrorStatus(err error) int {
	if err == errNoProvider {
		return 401
	}
	for _, name := range m.names {
		if status := m.providers[name].ErrorStatus(err); status != 502 {
			return status
		}
	}
	return 502
}
