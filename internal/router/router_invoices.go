package router

import "github.com/gin-gonic/gin"

func (ro *Routes) InvoiceRoutes(r *gin.Engine) {
	routes := r.Group("/api/v1/invoices")
	{
		routes.POST("/sync", ro.Mail.WithSession(ro.Invoices.Sync))
		routes.GET("", ro.Mail.WithSession(ro.Invoices.List))
		routes.GET("/summary", ro.Mail.WithSession(ro.Invoices.Summary))
		routes.GET("/export", ro.Mail.WithSession(ro.Invoices.Export))
		routes.PATCH("", ro.Mail.WithSession(ro.Invoices.UpdateMany))
		routes.PATCH("/:id", ro.Mail.WithSession(ro.Invoices.Update))
	}

	// Archivos guardados "en la nube": copias de los adjuntos del correo.
	files := r.Group("/api/v1/files")
	{
		files.POST("", ro.Mail.WithSession(ro.Invoices.SaveFile))
		files.GET("", ro.Mail.WithSession(ro.Invoices.ListFiles))
		files.GET("/:id", ro.Mail.WithSession(ro.Invoices.DownloadFile))
		files.PATCH("/:id", ro.Mail.WithSession(ro.Invoices.MoveFile))
		files.DELETE("/:id", ro.Mail.WithSession(ro.Invoices.DeleteFile))
	}

	// Carpetas creadas a mano por el usuario.
	folders := r.Group("/api/v1/folders")
	{
		folders.POST("", ro.Mail.WithSession(ro.Invoices.CreateFolder))
		folders.DELETE("", ro.Mail.WithSession(ro.Invoices.DeleteFolder))
	}
}
