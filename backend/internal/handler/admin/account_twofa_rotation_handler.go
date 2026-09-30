package admin

import (
	"net/http"

	"github.com/Wei-Shaw/sub2api/internal/pkg/response"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
)

func (h *AccountTokenGuardHandler) StartTwoFARotation(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 16<<10)
	var req service.AccountTwoFARotationRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		credentialSubmissionInvalid(c, "2FA 任务格式不正确")
		return
	}
	if err := service.ValidateAccountTwoFARotationRequest(req); err != nil {
		credentialSubmissionInvalid(c, err.Error())
		return
	}
	job, err := h.svc.StartTwoFARotation(c.Request.Context(), req)
	if err != nil {
		credentialSubmissionError(c, err)
		return
	}
	response.Accepted(c, job)
}

func (h *AccountTokenGuardHandler) TwoFARotationJobs(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	jobs, err := h.svc.TwoFARotationJobs(c.Request.Context())
	if err != nil {
		response.Error(c, http.StatusServiceUnavailable, err.Error())
		return
	}
	response.Success(c, gin.H{"jobs": jobs})
}

func (h *AccountTokenGuardHandler) VerifyTwoFARotation(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	job, err := h.svc.RetryTwoFARotation(c.Request.Context(), c.Param("id"))
	if err != nil {
		response.Error(c, http.StatusServiceUnavailable, err.Error())
		return
	}
	response.Accepted(c, job)
}

func (h *AccountTokenGuardHandler) TwoFARotationResult(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	result, err := h.svc.TwoFARotationResult(c.Request.Context(), c.Param("id"))
	if err != nil {
		response.Error(c, http.StatusServiceUnavailable, err.Error())
		return
	}
	response.Success(c, result)
}
