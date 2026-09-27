package service

import (
	"errors"
	"net/http"

	"github.com/gin-gonic/gin"
)

// Auxiliary APIs do not share the BPS Responses wire contract. Keep the selected
// channel explicit instead of executing a different provider's operation.
func rejectExcelBPSNativeEndpoint(c *gin.Context, message string) error {
	SetActualOpenAIUpstreamEndpoint(c, "/basispoints/api/responses")
	c.Header("X-Codex2API-Upstream", "basispoints")
	c.JSON(http.StatusBadRequest, gin.H{"error": gin.H{
		"type": "invalid_request_error", "code": "basispoints_endpoint_unsupported", "message": message,
	}})
	return errors.New("basispoints_endpoint_unsupported")
}
