package admin

import (
	"github.com/Wei-Shaw/sub2api/internal/pkg/response"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"net/http"
)

func (h *AccountTokenGuardHandler) StartSessionLogout(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 16<<10)
	var req service.AccountSessionLogoutRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "会话退出任务格式不正确")
		return
	}
	if err := service.ValidateAccountSessionLogoutRequest(req); err != nil {
		response.BadRequest(c, err.Error())
		return
	}
	job, err := h.svc.StartSessionLogout(c.Request.Context(), req)
	if err != nil {
		response.Error(c, http.StatusServiceUnavailable, err.Error())
		return
	}
	response.Accepted(c, job)
}

func (h *AccountTokenGuardHandler) SessionLogoutJobs(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	jobs, err := h.svc.SessionLogoutJobs(c.Request.Context())
	if err != nil {
		response.Error(c, http.StatusServiceUnavailable, err.Error())
		return
	}
	response.Success(c, gin.H{"jobs": jobs})
}
