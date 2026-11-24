package main

import (
	"context"
	"log"

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

	r.Mail = mail
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
