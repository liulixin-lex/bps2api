package admin

import (
	"errors"
	"net/http"

	"github.com/Wei-Shaw/sub2api/internal/pkg/response"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
)

func credentialSubmissionError(c *gin.Context, err error) {
	var rejected *service.CredentialSubmissionRejection
	if errors.As(err, &rejected) {
		response.ErrorWithDetails(c, http.StatusConflict, rejected.Error(), rejected.Reason, map[string]string{
			"submission": "not_accepted",
		})
		return
	}
	response.Error(c, http.StatusServiceUnavailable, err.Error())
}

func credentialSubmissionInvalid(c *gin.Context, message string) {
	// Handler validation runs before calling the worker.
	response.ErrorWithDetails(c, http.StatusBadRequest, message, "credential_submission_invalid", map[string]string{
		"submission": "not_accepted",
	})
}
