package router

import (
	mail_outlook "github.com/Esau21/Go-smpt-dte/internal/mail/outlook"
	"github.com/Esau21/Go-smpt-dte/internal/validations"
	"github.com/gin-gonic/gin"
)

func (ro *Routes) MailRoutes(r *gin.Engine) {
	callbackPath, err := validations.RequiredEnv("CALLBACK_PATH")

	if err != nil {
		callbackPath = "/api/auth/google/callback"
	}
	r.GET(callbackPath, ro.Mail.Callback)
	// Regreso del login de Microsoft (solo responde si Outlook está activo).
	r.GET(mail_outlook.CallbackPath, ro.Mail.Callback)
	routes := r.Group("/api/v1/mail")
	{
		// Con qué proveedores se puede iniciar sesión; no requiere sesión.
		routes.GET("/providers", func(c *gin.Context) {
			c.JSON(200, gin.H{"providers": ro.Mail.Providers()})
		})
		routes.GET("/auth", ro.Mail.Login)
		routes.DELETE("/session", ro.Mail.Logout)
		routes.GET("/me", ro.Mail.WithSession(ro.Mail.Me))
		routes.GET("/messages", ro.Mail.WithSession(ro.Mail.ListMessages))
		routes.GET("/messages/:id", ro.Mail.WithSession(ro.Mail.GetMessage))
		routes.GET("/messages/:id/dte", ro.Mail.WithSession(ro.Mail.GetDTE))
		routes.GET("/messages/:id/attachments/:index", ro.Mail.WithSession(ro.Mail.GetAttachment))
	}

}
