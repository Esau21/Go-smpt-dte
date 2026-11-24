package router

import (
	"log"
	"strings"

	"github.com/Esau21/Go-smpt-dte/internal/mail"
	"github.com/Esau21/Go-smpt-dte/internal/validations"
	"github.com/gin-gonic/gin"
)

type Routes struct {
	Routes *gin.Engine
	Mail   mail.IMail
}

func (r *Routes) SetupRouter() *gin.Engine {
	r.Routes = gin.Default()

	r.SetupCors()

	r.HealthCheckRoutes()
	r.MailRoutes(r.Routes)

	return r.Routes
}

func (r *Routes) Run() {
	const defaultPort = "8080"
	port, err := validations.RequiredEnv("PORT")
	if err != nil {
		port = defaultPort
	}
	if !strings.HasPrefix(port, ":") {
		port = ":" + port
	}
	if err := r.Routes.Run(port); err != nil {
		log.Fatal("Error: ", err)
	}
}
