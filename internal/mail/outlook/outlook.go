// Package mail_outlook conecta cuentas de Outlook / Microsoft 365 usando
// Microsoft Graph, con acceso de solo lectura al correo.
//
// Responde igual que el proveedor de Gmail: las mismas rutas devuelven el
// mismo formato, así el resto de la aplicación no distingue de dónde viene
// el correo.
package mail_outlook

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/Esau21/Go-smpt-dte/internal/dte"
	mail_gmail "github.com/Esau21/Go-smpt-dte/internal/mail/gmail"
	"github.com/Esau21/Go-smpt-dte/internal/mailbox"
	"github.com/Esau21/Go-smpt-dte/internal/validations"
	"github.com/gin-gonic/gin"
	"github.com/jellydator/ttlcache/v3"
	"github.com/jhillyerd/enmime/v2"
	"golang.org/x/oauth2"
)

const (
	// CallbackPath es a donde Microsoft devuelve al usuario tras el login.
	// Hay que registrarla en Azure: <PUBLIC_ORIGIN>/api/auth/microsoft/callback
	CallbackPath = "/api/auth/microsoft/callback"

	graph         = "https://graph.microsoft.com/v1.0"
	flowTTL       = 10 * time.Minute
	sessionTTL    = 8 * time.Hour
	flowCookie    = "outlook_flow"
	sessionCookie = "outlook_session"
	contextKey    = "internal/mail/outlook.session"

	// maxMessageSize limita cuánto se descarga de un correo completo.
	maxMessageSize = 40 << 20
)

type session struct {
	id    string
	email string
	http  *http.Client // añade el token y lo renueva solo
}

type Client struct {
	oauth      oauth2.Config
	flows      *ttlcache.Cache[string, string] // estado -> verificador PKCE
	sessions   *ttlcache.Cache[string, session]
	afterLogin string
	secure     bool
}

func newCache[V any](ttl time.Duration) *ttlcache.Cache[string, V] {
	cache := ttlcache.New(
		ttlcache.WithTTL[string, V](ttl),
		ttlcache.WithDisableTouchOnHit[string, V](),
		ttlcache.WithCapacity[string, V](10000),
	)
	go cache.Start()
	return cache
}

// Setup prepara el proveedor. Necesita la aplicación registrada en Azure:
// OUTLOOK_CLIENT_ID y OUTLOOK_CLIENT_SECRET. OUTLOOK_TENANT es opcional
// ("common" acepta cuentas personales y de empresa).
func Setup(origin, afterLogin string) (*Client, error) {
	id, err := validations.RequiredEnv("OUTLOOK_CLIENT_ID")
	if err != nil {
		return nil, err
	}
	secret, err := validations.RequiredEnv("OUTLOOK_CLIENT_SECRET")
	if err != nil {
		return nil, err
	}
	tenant, err := validations.RequiredEnv("OUTLOOK_TENANT")
	if err != nil {
		tenant = "common"
	}

	base := "https://login.microsoftonline.com/" + url.PathEscape(tenant) + "/oauth2/v2.0"
	return &Client{
		oauth: oauth2.Config{
			ClientID:     id,
			ClientSecret: secret,
			Endpoint: oauth2.Endpoint{
				AuthURL:   base + "/authorize",
				TokenURL:  base + "/token",
				AuthStyle: oauth2.AuthStyleInParams,
			},
			RedirectURL: origin + CallbackPath,
			// offline_access permite renovar el acceso sin volver a pedir login.
			Scopes: []string{"openid", "email", "offline_access", "User.Read", "Mail.Read"},
		},
		flows:      newCache[string](flowTTL),
		sessions:   newCache[session](sessionTTL),
		afterLogin: afterLogin,
		secure:     strings.HasPrefix(origin, "https://"),
	}, nil
}

func (oc *Client) cookie(c *gin.Context, name, value string, seconds int) {
	c.SetSameSite(http.SameSiteLaxMode)
	c.SetCookie(name, value, seconds, "/api", "", oc.secure, true)
}

// Providers lista los proveedores con los que se puede iniciar sesión.
func (*Client) Providers() []string {
	return []string{"outlook"}
}

/* ---------- sesión ---------- */

func (oc *Client) Login(c *gin.Context) {
	state := rand.Text()
	verifier := oauth2.GenerateVerifier()
	oc.flows.Set(state, verifier, flowTTL)
	oc.cookie(c, flowCookie, state, int(flowTTL.Seconds()))

	c.Redirect(http.StatusFound, oc.oauth.AuthCodeURL(state,
		oauth2.S256ChallengeOption(verifier),
		oauth2.SetAuthURLParam("prompt", "select_account"),
	))
}

func (oc *Client) Callback(c *gin.Context) {
	state, err := c.Cookie(flowCookie)
	if err != nil || state == "" ||
		subtle.ConstantTimeCompare([]byte(state), []byte(c.Query("state"))) != 1 {
		validations.Fail(c, 400, "invalid_oauth_state")
		return
	}
	entry, ok := oc.flows.GetAndDelete(state)
	oc.cookie(c, flowCookie, "", -1)
	if !ok {
		validations.Fail(c, 400, "expired_or_used_login")
		return
	}
	if c.Query("error") != "" || c.Query("code") == "" {
		validations.Fail(c, 403, "authorization_not_completed")
		return
	}

	ctx, cancel := context.WithTimeout(c.Request.Context(), 15*time.Second)
	defer cancel()
	token, err := oc.oauth.Exchange(ctx, c.Query("code"), oauth2.VerifierOption(entry.Value()))
	if err != nil {
		validations.Fail(c, oc.ErrorStatus(err), "code_exchange_failed")
		return
	}

	// El cliente HTTP vive más que esta petición: renueva el token cuando vence.
	clientCtx := context.WithValue(context.Background(), oauth2.HTTPClient,
		&http.Client{Timeout: 30 * time.Second})
	s := session{http: oc.oauth.Client(clientCtx, token)}

	// La identidad se le pregunta a Graph con el token recién obtenido.
	var me struct {
		ID                string `json:"id"`
		Mail              string `json:"mail"`
		UserPrincipalName string `json:"userPrincipalName"`
	}
	if err := s.get(ctx, graph+"/me?$select=id,mail,userPrincipalName", &me); err != nil || me.ID == "" {
		validations.Fail(c, 401, "invalid_microsoft_identity")
		return
	}
	s.id = me.ID
	s.email = strings.ToLower(me.Mail)
	if s.email == "" {
		s.email = strings.ToLower(me.UserPrincipalName)
	}
	if s.email == "" {
		validations.Fail(c, 403, "verified_email_required")
		return
	}

	old, _ := c.Cookie(sessionCookie)
	oc.sessions.Delete(old)

	id := rand.Text()
	oc.sessions.Set(id, s, sessionTTL)
	oc.cookie(c, sessionCookie, id, int(sessionTTL.Seconds()))
	c.Redirect(http.StatusSeeOther, oc.afterLogin)
}

func (oc *Client) Logout(c *gin.Context) {
	id, _ := c.Cookie(sessionCookie)
	oc.sessions.Delete(id)
	state, _ := c.Cookie(flowCookie)
	oc.flows.Delete(state)

	oc.cookie(c, flowCookie, "", -1)
	oc.cookie(c, sessionCookie, "", -1)
	c.Status(http.StatusNoContent)
}

// HasSession dice si esta petición trae una sesión de Outlook vigente.
func (oc *Client) HasSession(c *gin.Context) bool {
	id, _ := c.Cookie(sessionCookie)
	return id != "" && oc.sessions.Get(id) != nil
}

func (oc *Client) WithSession(next gin.HandlerFunc) gin.HandlerFunc {
	return func(c *gin.Context) {
		id, _ := c.Cookie(sessionCookie)
		entry := oc.sessions.Get(id)
		if entry == nil {
			validations.Fail(c, 401, "login_required")
			return
		}
		c.Set(contextKey, entry.Value())
		next(c)
		if c.Writer.Status() == http.StatusUnauthorized {
			oc.sessions.Delete(id)
		}
	}
}

var errNoSession = errors.New("no mail session")

func sessionOf(c *gin.Context) (session, bool) {
	value, _ := c.Get(contextKey)
	s, ok := value.(session)
	return s, ok
}

// need devuelve la sesión o responde 401.
func need(c *gin.Context) (session, bool) {
	s, ok := sessionOf(c)
	if !ok {
		c.Abort()
		validations.Fail(c, http.StatusUnauthorized, "login_required")
	}
	return s, ok
}

/* ---------- llamadas a Microsoft Graph ---------- */

type graphError struct{ status int }

func (e *graphError) Error() string { return fmt.Sprintf("microsoft graph: HTTP %d", e.status) }

func (s session) do(ctx context.Context, target string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return nil, err
	}
	// Identificadores que no cambian aunque el correo se mueva de carpeta.
	req.Header.Set("Prefer", `IdType="ImmutableId"`)
	res, err := s.http.Do(req)
	if err != nil {
		return nil, err
	}
	if res.StatusCode < 200 || res.StatusCode > 299 {
		res.Body.Close()
		return nil, &graphError{status: res.StatusCode}
	}
	return res, nil
}

func (s session) get(ctx context.Context, target string, out any) error {
	res, err := s.do(ctx, target)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	return json.NewDecoder(res.Body).Decode(out)
}

func (s session) raw(ctx context.Context, target string) ([]byte, error) {
	res, err := s.do(ctx, target)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	return io.ReadAll(io.LimitReader(res.Body, maxMessageSize))
}

// envelope descarga un correo completo (formato MIME) y lo interpreta.
func (s session) envelope(ctx context.Context, id string) (*enmime.Envelope, error) {
	content, err := s.raw(ctx, graph+"/me/messages/"+url.PathEscape(id)+"/$value")
	if err != nil {
		return nil, err
	}
	mail, err := enmime.ReadEnvelope(strings.NewReader(string(content)))
	if err != nil {
		return nil, errUnreadable
	}
	return mail, nil
}

var errUnreadable = errors.New("unreadable message")

// ErrorStatus traduce un error del proveedor al código HTTP que le corresponde.
func (*Client) ErrorStatus(err error) int {
	var graphErr *graphError
	var tokenErr *oauth2.RetrieveError
	switch {
	case err == errNoSession:
		return 401
	case errors.As(err, &tokenErr):
		return 401 // el permiso fue revocado o el token ya no se puede renovar
	case errors.Is(err, context.DeadlineExceeded):
		return 504
	case errors.As(err, &graphErr):
		switch graphErr.status {
		case 400, 401, 403, 404, 429:
			return graphErr.status
		}
	}
	return 502
}

func (oc *Client) fail(c *gin.Context, err error) {
	if err == errUnreadable {
		validations.Fail(c, 422, "unreadable_message")
		return
	}
	status := oc.ErrorStatus(err)
	validations.Fail(c, status, http.StatusText(status))
}

/* ---------- búsqueda ---------- */

var queryToken = regexp.MustCompile(`(\w+):\(([^)]*)\)|(\w+):(\S+)|(\S+)`)

// toKQL traduce la búsqueda que arma el front (sintaxis de Gmail) a la de
// Outlook (KQL). Por ejemplo:
//
//	filename:json from:(siman) after:2026/11/01
//	→ attachment:json from:siman received>=2026-11-01
func toKQL(q string, now time.Time) string {
	var out []string
	date := func(value string) string { return strings.ReplaceAll(value, "/", "-") }

	for _, m := range queryToken.FindAllStringSubmatch(q, -1) {
		key, value := m[1], m[2]
		if key == "" {
			key, value = m[3], m[4]
		}
		// Las comillas y barras romperían la consulta: se quitan.
		value = strings.NewReplacer(`"`, "", `\`, "").Replace(value)

		switch strings.ToLower(key) {
		case "":
			if word := strings.NewReplacer(`"`, "", `\`, "").Replace(m[5]); word != "" {
				out = append(out, word)
			}
		case "filename":
			out = append(out, "attachment:"+value)
		case "from", "subject":
			for _, word := range strings.Fields(value) {
				out = append(out, strings.ToLower(key)+":"+word)
			}
		case "after":
			out = append(out, "received>="+date(value))
		case "before":
			out = append(out, "received<"+date(value))
		case "newer_than":
			if days, err := strconv.Atoi(strings.TrimSuffix(value, "d")); err == nil {
				out = append(out, "received>="+now.AddDate(0, 0, -days).Format("2006-01-02"))
			}
		case "has":
			if value == "attachment" {
				out = append(out, "hasattachment:true")
			}
		default:
			out = append(out, value)
		}
	}
	return strings.Join(out, " ")
}

const listFields = "id,conversationId,subject,bodyPreview,isRead,receivedDateTime,from"

// listURL arma la consulta de una página de correos. Con búsqueda, Graph ya
// devuelve del más reciente al más antiguo y no admite ordenar.
func listURL(kql string, limit int) string {
	params := url.Values{"$top": {strconv.Itoa(limit)}, "$select": {listFields}}
	if kql == "" {
		params.Set("$orderby", "receivedDateTime desc")
		return graph + "/me/mailFolders/inbox/messages?" + params.Encode()
	}
	params.Set("$search", `"`+kql+`"`)
	return graph + "/me/messages?" + params.Encode()
}

// La "página siguiente" de Graph es una URL completa. Viaja al front
// codificada, y al volver solo se acepta si apunta a Graph.
func encodePage(next string) string {
	if next == "" {
		return ""
	}
	return base64.RawURLEncoding.EncodeToString([]byte(next))
}

func decodePage(token string) (string, bool) {
	raw, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil || !strings.HasPrefix(string(raw), graph+"/") {
		return "", false
	}
	return string(raw), true
}

type graphMessage struct {
	ID             string `json:"id"`
	ConversationID string `json:"conversationId"`
	Subject        string `json:"subject"`
	BodyPreview    string `json:"bodyPreview"`
	IsRead         bool   `json:"isRead"`
	Received       string `json:"receivedDateTime"`
	From           struct {
		EmailAddress struct {
			Name    string `json:"name"`
			Address string `json:"address"`
		} `json:"emailAddress"`
	} `json:"from"`
}

func (m graphMessage) from() string {
	name, address := m.From.EmailAddress.Name, m.From.EmailAddress.Address
	if name == "" || name == address {
		return address
	}
	return name + " <" + address + ">"
}

type graphPage struct {
	Value    []graphMessage `json:"value"`
	NextLink string         `json:"@odata.nextLink"`
}

func (s session) page(ctx context.Context, q, pageToken string, limit int) (graphPage, error) {
	target := listURL(toKQL(q, time.Now()), limit)
	if pageToken != "" {
		next, ok := decodePage(pageToken)
		if !ok {
			return graphPage{}, &graphError{status: 400}
		}
		target = next
	}
	var page graphPage
	err := s.get(ctx, target, &page)
	return page, err
}

/* ---------- rutas del correo ---------- */

func (*Client) Me(c *gin.Context) {
	if s, ok := need(c); ok {
		c.JSON(200, gin.H{"id": s.id, "email": s.email, "provider": "outlook"})
	}
}

func (oc *Client) ListMessages(c *gin.Context) {
	s, ok := need(c)
	if !ok {
		return
	}
	limit, err := strconv.Atoi(c.DefaultQuery("limit", "10"))
	if err != nil || limit < 1 || limit > 25 {
		validations.Fail(c, 400, "limit_must_be_1_to_25")
		return
	}

	ctx, cancel := context.WithTimeout(c.Request.Context(), 30*time.Second)
	defer cancel()
	page, err := s.page(ctx, c.Query("q"), c.Query("pageToken"), limit)
	if err != nil {
		oc.fail(c, err)
		return
	}

	items := make([]gin.H, len(page.Value))
	for i, m := range page.Value {
		items[i] = gin.H{
			"id":       m.ID,
			"threadId": m.ConversationID,
			"from":     m.from(),
			"subject":  m.Subject,
			"date":     m.Received,
			"snippet":  m.BodyPreview,
			"unread":   !m.IsRead,
		}
	}
	c.JSON(200, gin.H{"messages": items, "nextPageToken": encodePage(page.NextLink)})
}

func (oc *Client) GetMessage(c *gin.Context) {
	s, ok := need(c)
	if !ok {
		return
	}
	mail, err := s.envelope(c.Request.Context(), c.Param("id"))
	if err != nil {
		oc.fail(c, err)
		return
	}
	c.JSON(200, mail_gmail.EnvelopeJSON(c.Param("id"), "", mail))
}

func (oc *Client) GetDTE(c *gin.Context) {
	s, ok := need(c)
	if !ok {
		return
	}
	mail, err := s.envelope(c.Request.Context(), c.Param("id"))
	if err != nil {
		oc.fail(c, err)
		return
	}
	content, found := mail_gmail.FindDTE(mail)
	if !found {
		validations.Fail(c, 404, "dte_not_found")
		return
	}
	doc, err := dte.Parse(content)
	if err != nil {
		validations.Fail(c, 422, "invalid_dte")
		return
	}
	c.JSON(200, doc)
}

func (oc *Client) GetAttachment(c *gin.Context) {
	s, ok := need(c)
	if !ok {
		return
	}
	index, err := strconv.Atoi(c.Param("index"))
	if err != nil || index < 0 {
		validations.Fail(c, 400, "invalid_attachment")
		return
	}
	mail, err := s.envelope(c.Request.Context(), c.Param("id"))
	if err != nil {
		oc.fail(c, err)
		return
	}
	mail_gmail.ServeAttachment(c, mail, index)
}

/* ---------- lo que usa el módulo de facturas ---------- */

func (*Client) Account(c *gin.Context) (mailbox.Account, bool) {
	s, ok := sessionOf(c)
	if !ok {
		return mailbox.Account{}, false
	}
	return mailbox.Account{Provider: "outlook", ID: s.id, Email: s.email}, true
}

// ListDTEMessages devuelve una página de correos con un .json adjunto.
func (*Client) ListDTEMessages(c *gin.Context, pageToken string, limit int) ([]string, string, error) {
	s, ok := sessionOf(c)
	if !ok {
		return nil, "", errNoSession
	}
	page, err := s.page(c.Request.Context(), "filename:json", pageToken, limit)
	if err != nil {
		return nil, "", err
	}
	ids := make([]string, len(page.Value))
	for i, m := range page.Value {
		ids[i] = m.ID
	}
	return ids, encodePage(page.NextLink), nil
}

// FetchDTE devuelve el .json adjunto de un correo, o nil si no trae ninguno.
// Igual que con Gmail, no baja el correo entero: lista los adjuntos y
// descarga solo el .json.
func (*Client) FetchDTE(c *gin.Context, id string) (*mailbox.Message, error) {
	s, ok := sessionOf(c)
	if !ok {
		return nil, errNoSession
	}
	ctx := c.Request.Context()
	base := graph + "/me/messages/" + url.PathEscape(id)

	var attachments struct {
		Value []struct {
			ID   string `json:"id"`
			Name string `json:"name"`
			Size int    `json:"size"`
		} `json:"value"`
	}
	if err := s.get(ctx, base+"/attachments?$select=id,name,size", &attachments); err != nil {
		return nil, err
	}
	attachmentID := ""
	for _, a := range attachments.Value {
		if strings.HasSuffix(strings.ToLower(a.Name), ".json") && a.Size <= 5<<20 {
			attachmentID = a.ID
			break
		}
	}
	if attachmentID == "" {
		return nil, nil
	}

	content, err := s.raw(ctx, base+"/attachments/"+url.PathEscape(attachmentID)+"/$value")
	if err != nil {
		return nil, err
	}
	var m graphMessage
	if err := s.get(ctx, base+"?$select=id,subject,from", &m); err != nil {
		return nil, err
	}
	return &mailbox.Message{ID: id, From: m.from(), Subject: m.Subject, DTE: content}, nil
}

// FetchAttachment descarga un adjunto con los datos del correo del que salió.
func (*Client) FetchAttachment(c *gin.Context, id string, index int) (*mailbox.Attachment, error) {
	s, ok := sessionOf(c)
	if !ok {
		return nil, errNoSession
	}
	mail, err := s.envelope(c.Request.Context(), id)
	if err == errUnreadable {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	parts := mail_gmail.Files(mail)
	if index < 0 || index >= len(parts) {
		return nil, nil
	}
	return &mailbox.Attachment{
		MessageID: id,
		Filename:  parts[index].FileName,
		Content:   parts[index].Content,
		From:      mail.GetHeader("From"),
		Subject:   mail.GetHeader("Subject"),
	}, nil
}
