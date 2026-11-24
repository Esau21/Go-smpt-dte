package validations

import "github.com/gin-gonic/gin"

func Fail(c *gin.Context, status int, message string) {
	c.AbortWithStatusJSON(status, gin.H{"error": message})
}
