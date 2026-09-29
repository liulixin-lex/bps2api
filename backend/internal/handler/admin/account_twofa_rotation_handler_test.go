package admin

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

func TestTwoFARotationHandlerRejectsInvalidBodiesWithoutLeakingCredentials(t *testing.T) {
	gin.SetMode(gin.TestMode)
	h := &AccountTokenGuardHandler{}
	for _, body := range []string{
		"{invalid-secret-json", "{\"confirmed\":false,\"password\":\"private-password\"}",
		"{\"confirmed\":true,\"request_id\":\"../escape\",\"password\":\"private-password\"}",
		"{\"confirmed\":true,\"password\":\"" + strings.Repeat("x", 17<<10) + "\"}",
	} {
		recorder := httptest.NewRecorder()
		context, _ := gin.CreateTestContext(recorder)
		context.Request = httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body))
		context.Request.Header.Set("Content-Type", "application/json")
		h.StartTwoFARotation(context)
		if recorder.Code != http.StatusBadRequest {
			t.Errorf("status=%d", recorder.Code)
		}
		if recorder.Header().Get("Cache-Control") != "no-store" {
			t.Error("missing no-store")
		}
		if strings.Contains(recorder.Body.String(), "private-password") || strings.Contains(recorder.Body.String(), "invalid-secret-json") {
			t.Fatal("request body echoed")
		}
	}
}
