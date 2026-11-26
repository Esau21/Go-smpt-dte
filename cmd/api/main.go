package main

import (
	"context"
	"log"

	"github.com/Esau21/Go-smpt-dte/internal/bucket"
	"github.com/Esau21/Go-smpt-dte/internal/invoices"
	"github.com/Esau21/Go-smpt-dte/internal/mail"
	"github.com/Esau21/Go-smpt-dte/internal/store"
	"github.com/Esau21/Go-smpt-dte/internal/router"
	"github.com/Esau21/Go-smpt-dte/internal/validations"
)

func runApp(ctx context.Context) error {
	r := &router.Routes{}

	origin, err := validations.RequiredEnv("PUBLIC_ORIGIN")

	if err != nil {
		return err
	}

	afterLogin, err := validations.RequiredEnv("AFTER_LOGIN_PATH")

	if err != nil {
		return err
	}

	mail, err := mail.NewMail(origin, afterLogin)

	if err != nil {
		return err
	}

	// La base de datos guarda las facturas. Si no está disponible, la API
	// arranca igual: el correo funciona y las rutas de facturas responden 503.
	var db *store.Store
	if dsn, err := validations.RequiredEnv("DB_STRING"); err != nil {
		log.Println("Warning: DB_STRING no configurado; las facturas quedan deshabilitadas")
	} else if db, err = store.Open(ctx, dsn); err != nil {
		log.Println("Warning: base de datos no disponible; las facturas quedan deshabilitadas:", err)
		db = nil
	}

	r.Mail = mail
	handlers := &invoices.Handlers{Store: db, Mail: mail}

	// El bucket guarda los archivos que el usuario manda "a la nube". Sin
	// bucket, esos archivos quedan en la base de datos.
	if name, err := validations.RequiredEnv("FILES_BUCKET"); err != nil {
		log.Println("Warning: FILES_BUCKET no configurado; los archivos se guardan en la base de datos")
	} else if files, err := bucket.Open(ctx, name); err != nil {
		log.Println("Warning: bucket no disponible; los archivos se guardan en la base de datos:", err)
	} else {
		handlers.Bucket = files
	}
	r.Invoices = handlers
	r.Routes = r.SetupRouter()

	r.Run()

	return nil
}

func main() {
	ctx := context.Background()
	if err := runApp(ctx); err != nil {
		log.Fatal("Error: ", err)
		return
	}
}
