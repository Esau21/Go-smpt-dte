package mail_gmail

import (
	"net/http"

	"github.com/Esau21/Go-smpt-dte/internal/validations"
	"github.com/gin-gonic/gin"
)

func gmailFailure(c *gin.Context, err error) {
	status := googleStatus(err)
	validations.Fail(c, status, http.StatusText(status))
}
