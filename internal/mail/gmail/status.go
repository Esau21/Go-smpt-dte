package mail_gmail

import (
	"context"
	"errors"
	"slices"

	"golang.org/x/oauth2"
	"google.golang.org/api/googleapi"
)

func googleStatus(err error) int {
	var tokenErr *oauth2.RetrieveError
	var apiErr *googleapi.Error

	switch {
	case errors.As(err, &tokenErr) && tokenErr.ErrorCode == "invalid_grant":
		return 401
	case errors.Is(err, context.DeadlineExceeded):
		return 504
	case errors.As(err, &apiErr):
		for _, detail := range apiErr.Errors {
			if detail.Reason == "rateLimitExceeded" || detail.Reason == "userRateLimitExceeded" {
				return 429
			}
		}
		if slices.Contains([]int{400, 401, 403, 404, 429}, apiErr.Code) {
			return apiErr.Code
		}
	}
	return 502
}
