package main

import (
	"context"
	"log"

	"github.com/Esau21/Go-smpt-dte/internal/bucket"
	"github.com/Esau21/Go-smpt-dte/internal/invoices"
	"github.com/Esau21/Go-smpt-dte/internal/mail"
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

	// Todo lo que la app guarda (facturas, notas y archivos) vive en un
	// bucket. Si no está disponible, la API arranca igual: el correo
	// funciona y las rutas de facturas y archivos responden 503.
	var files invoices.Files
	if name, err := validations.RequiredEnv("FILES_BUCKET"); err != nil {
		log.Println("Warning: FILES_BUCKET no configurado; facturas y archivos quedan deshabilitados")
	} else if opened, err := bucket.Open(ctx, name); err != nil {
		log.Println("Warning: bucket no disponible; facturas y archivos quedan deshabilitados:", err)
	} else {
		files = opened
	}

	r.Mail = mail
	r.Invoices = invoices.New(files, mail)
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
