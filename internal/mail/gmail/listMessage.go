package mail_gmail

import (
	"context"
	"net/http"
	"slices"
	"strconv"
	"time"

	"github.com/Esau21/Go-smpt-dte/internal/validations"
	"github.com/gin-gonic/gin"
	"golang.org/x/sync/errgroup"
)

func (*GmailClient) ListMessages(c *gin.Context) {
	value, _ := c.Get(sessionContextKey)
	s, ok := value.(session)
	if !ok {
		c.Abort()
		validations.Fail(c, http.StatusUnauthorized, "login_required")
		return
	}

	listMessages(c, s)
}

func listMessages(c *gin.Context, s session) {
	limit, err := strconv.Atoi(c.DefaultQuery("limit", "10"))
	if err != nil || limit < 1 || limit > 25 {
		validations.Fail(c, 400, "limit_must_be_1_to_25")
		return
	}

	ctx, cancel := context.WithTimeout(c.Request.Context(), 30*time.Second)
	defer cancel()

	messages := s.gmail.Users.Messages
	call := messages.List("me").
		MaxResults(int64(limit)).
		Q(c.Query("q")).
		PageToken(c.Query("pageToken"))

	if label := c.DefaultQuery("label", "INBOX"); label != "" {
		call.LabelIds(label)
	}

	page, err := call.Context(ctx).Do()
	if err != nil {
		gmailFailure(c, err)
		return
	}

	items := make([]gin.H, len(page.Messages))
	group, ctx := errgroup.WithContext(ctx)
	group.SetLimit(4)

	for i, ref := range page.Messages {
		group.Go(func() error {
			m, err := messages.Get("me", ref.Id).
				Format("metadata").
				MetadataHeaders("From", "Subject", "Date").
				Context(ctx).Do()

			if err != nil {
				if googleStatus(err) == 404 {
					return nil
				}
				return err
			}
			items[i] = summary(m)
			return nil
		})
	}

	if err := group.Wait(); err != nil {
		gmailFailure(c, err)
		return
	}

	items = slices.DeleteFunc(items, func(item gin.H) bool { return item == nil })
	c.JSON(200, gin.H{
		"messages":      items,
		"nextPageToken": page.NextPageToken,
	})
}
