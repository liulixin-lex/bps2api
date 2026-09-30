package admin

import (
	"encoding/json"
	"errors"
	"net/http/httptest"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/pkg/response"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
)

func TestTwoFASubmissionHandlerSeparatesRejectedFromUncertain(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, rejected := range []bool{true, false} {
		recorder := httptest.NewRecorder()
		ctx, _ := gin.CreateTestContext(recorder)
		var err error = errors.New("通信未确认")
		if rejected {
			err = &service.CredentialSubmissionRejection{Reason: "account_has_unresolved_job"}
		}
		credentialSubmissionError(ctx, err)
		var result response.Response
		if json.Unmarshal(recorder.Body.Bytes(), &result) != nil {
			t.Fatal("invalid envelope")
		}
		if rejected {
			if recorder.Code != 409 || result.Reason != "account_has_unresolved_job" || result.Metadata["submission"] != "not_accepted" {
				t.Fatal("lost definite rejection metadata")
			}
		} else if recorder.Code != 503 || result.Reason != "" || result.Metadata != nil {
			t.Fatal("uncertain error became retryable as a new task")
		}
	}
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	credentialSubmissionInvalid(ctx, "invalid input")
	var result response.Response
	if json.Unmarshal(recorder.Body.Bytes(), &result) != nil || recorder.Code != 400 || result.Reason != "credential_submission_invalid" || result.Metadata["submission"] != "not_accepted" {
		t.Fatal("validation rejection lost")
	}
}
