package mail_gmail

import (
	"github.com/gin-gonic/gin"
	"google.golang.org/api/gmail/v1"
)

func fields(m *gmail.Message, header func(string) string) gin.H {
	return gin.H{
		"id":       m.Id,
		"threadId": m.ThreadId,
		"from":     header("From"),
		"subject":  header("Subject"),
		"date":     header("Date"),
	}
}
