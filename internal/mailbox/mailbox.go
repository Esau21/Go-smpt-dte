// Package mailbox define los tipos que comparten todos los proveedores de
// correo (Gmail hoy; Outlook y Yahoo después), para que el resto de la
// aplicación no dependa de ninguno en particular.
package mailbox

// Account identifica la cuenta de correo con sesión activa.
type Account struct {
	Provider string // "gmail", "outlook", ...
	ID       string // identificador estable del usuario en el proveedor
	Email    string
}

// Owner es la llave con la que se guardan los datos de esta cuenta.
func (a Account) Owner() string {
	return a.Provider + ":" + a.ID
}

// Attachment es un archivo adjunto de un correo, con su contenido.
type Attachment struct {
	MessageID string
	Filename  string
	Content   []byte
	From      string
	Subject   string
}

// Message es un correo que trae un DTE adjunto.
type Message struct {
	ID      string
	From    string
	Subject string
	DTE     []byte // contenido del .json adjunto
}
