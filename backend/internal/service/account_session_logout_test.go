package service

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestSessionLogoutValidationAndIsolatedProtocol(t *testing.T) {
	req := AccountSessionLogoutRequest{AccountTokenGuardReloginAccount: AccountTokenGuardReloginAccount{Email: "TEST@example.com", Password: " p|a,ss----word ", MFASecret: "JBSWY3DPEHPK3PXP"}, RequestID: rotationTestID, Confirmed: true}
	if err := ValidateAccountSessionLogoutRequest(req); err != nil {
		t.Fatal(err)
	}
	calls := 0
	svc := rotationTestService(t, func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.URL.Path != "/session-logout/jobs" || r.Header.Get("Authorization") != "Bearer "+rotationTestToken {
			t.Fatal("wrong worker route or authentication")
		}
		if r.Method == http.MethodPost {
			var received AccountSessionLogoutRequest
			if err := json.NewDecoder(r.Body).Decode(&received); err != nil {
				t.Fatal(err)
			}
			if received != req {
				t.Fatal("credentials were changed")
			}
			w.WriteHeader(http.StatusAccepted)
			_ = json.NewEncoder(w).Encode(map[string]any{"id": rotationTestID, "email": "test@example.com", "status": "queued", "password": "do-not-forward"})
		} else {
			_ = json.NewEncoder(w).Encode(map[string]any{"jobs": []map[string]any{{"id": rotationTestID, "email": "test@example.com", "status": "accepted", "password": "do-not-forward", "error_code": "private-token"}}})
		}
	})
	job, err := svc.StartSessionLogout(context.Background(), req)
	if err != nil || job.Status != "queued" {
		t.Fatalf("job %v error %v", job, err)
	}
	jobs, err := svc.SessionLogoutJobs(context.Background())
	if err != nil || len(jobs) != 1 || jobs[0].ErrorCode != "" {
		t.Fatalf("list error %v", err)
	}
	encoded, _ := json.Marshal(jobs)
	if strings.Contains(string(encoded), "do-not-forward") || strings.Contains(string(encoded), "private-token") {
		t.Fatal("worker secrets leaked")
	}
	req.Confirmed = false
	if _, err = svc.StartSessionLogout(context.Background(), req); err == nil || calls != 2 {
		t.Fatal("unconfirmed call reached worker")
	}
}

func TestSessionLogoutRejectsMismatchedTaskAndSanitizesUnknownState(t *testing.T) {
	req := AccountSessionLogoutRequest{AccountTokenGuardReloginAccount: AccountTokenGuardReloginAccount{Email: "test@example.com", Password: "private", MFASecret: "JBSWY3DPEHPK3PXP"}, RequestID: rotationTestID, Confirmed: true}
	svc := rotationTestService(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "{\"id\":\""+rotationTestID+"\",\"email\":\"other@example.com\",\"status\":\"accepted\"}")
	})
	if _, err := svc.StartSessionLogout(context.Background(), req); err == nil {
		t.Fatal("wrong account accepted")
	}
	job := AccountSessionLogoutJob{Status: "sensitive-worker-state", ErrorCode: "private-cookie"}
	sanitizeSessionLogoutJob(&job)
	if job.Status != "needs_review" || job.ErrorCode != "" {
		t.Fatal("unsafe diagnostic")
	}
}

func TestWorkspaceLoginCodesSurviveBothOperationAPIs(t *testing.T) {
	for _, code := range []string{"login_workspace_selection_failed", "login_session_incomplete"} {
		rotation := AccountTwoFARotationJob{ErrorCode: code}
		sanitizeTwoFARotationJob(&rotation)
		logout := AccountSessionLogoutJob{Status: "login_failed", ErrorCode: code}
		sanitizeSessionLogoutJob(&logout)
		if rotation.ErrorCode != code || logout.ErrorCode != code {
			t.Fatal("login completion diagnostic was discarded")
		}
	}
}

func TestSessionLogoutDeleteAndProgressProtocol(t *testing.T) {
	calls := 0
	svc := rotationTestService(t, func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.Method != http.MethodDelete || r.URL.Path != "/session-logout/jobs" {
			t.Fatal("wrong delete route")
		}
		var body AccountTwoFARotationDeleteRequest
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil || len(body.IDs) != 1 || body.IDs[0] != rotationTestID {
			t.Fatal("wrong selection")
		}
		_ = json.NewEncoder(w).Encode(AccountTwoFARotationDeleteResult{DeletedIDs: body.IDs})
	})
	result, err := svc.DeleteSessionLogoutJobs(context.Background(), AccountTwoFARotationDeleteRequest{IDs: []string{rotationTestID}})
	if err != nil || len(result.DeletedIDs) != 1 {
		t.Fatal("delete failed", err)
	}
	if _, err := svc.DeleteSessionLogoutJobs(context.Background(), AccountTwoFARotationDeleteRequest{}); err == nil || calls != 1 {
		t.Fatal("invalid request forwarded")
	}
	for _, phase := range []string{"mfa_retry", "verify_mfa", "logout_preflight", "revoking"} {
		logout := AccountSessionLogoutJob{Status: "logging_in", Phase: phase, Deletable: true}
		sanitizeSessionLogoutJob(&logout)
		rotation := AccountTwoFARotationJob{Status: "running", Phase: phase}
		sanitizeTwoFARotationJob(&rotation)
		if logout.Phase != phase || rotation.Phase != phase || logout.Deletable {
			t.Fatal("progress or active protection lost")
		}
	}
	for _, status := range []string{"accepted", "needs_review", "failed", "private-state"} {
		logout := AccountSessionLogoutJob{Status: status, Phase: "private-cookie"}
		sanitizeSessionLogoutJob(&logout)
		if logout.Phase != "" {
			t.Fatal("unsafe or terminal progress")
		}
	}
}
