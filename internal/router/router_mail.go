package router

import (
	"github.com/Esau21/Go-smpt-dte/internal/validations"
	"github.com/gin-gonic/gin"
)

func (ro *Routes) MailRoutes(r *gin.Engine) {
	callbackPath, err := validations.RequiredEnv("CALLBACK_PATH")

	if err != nil {
		callbackPath = "/api/auth/google/callback"
	}
	r.GET(callbackPath, ro.Mail.Callback)
	routes := r.Group("/api/v1/mail")
	{
		routes.GET("/auth", ro.Mail.Login)
		routes.DELETE("/session", ro.Mail.Logout)
		routes.GET("/me", ro.Mail.WithSession(ro.Mail.Me))
		routes.GET("/messages", ro.Mail.WithSession(ro.Mail.ListMessages))
		routes.GET("/messages/:id", ro.Mail.WithSession(ro.Mail.GetMessage))
	}

}
