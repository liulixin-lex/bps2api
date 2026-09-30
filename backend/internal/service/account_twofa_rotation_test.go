package service

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

const rotationTestID = "0123456789abcdef0123456789abcdef"
const rotationTestToken = "test-worker-token-with-at-least-32-characters"

func rotationTestService(t *testing.T, handler http.HandlerFunc) *AccountTokenGuardService {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	svc := &AccountTokenGuardService{httpClient: server.Client()}
	svc.config.Store(AccountTokenGuardConfig{TwoFARotationEndpoint: server.URL, TwoFARotationToken: rotationTestToken})
	return svc
}

func TestTwoFARotationConfiguration(t *testing.T) {
	for _, tt := range []struct {
		endpoint, token string
		valid           bool
	}{
		{"", "", true}, {"http://twofa-worker:8080", rotationTestToken, true},
		{"http://127.0.0.1:8080", rotationTestToken, true}, {"http://10.0.0.2", rotationTestToken, true},
		{"https://worker.example.com", rotationTestToken, true},
		{"http://worker.example.com", rotationTestToken, false},
		{"https://user:pass@worker.example.com", rotationTestToken, false},
		{"https://worker.example.com?token=secret", rotationTestToken, false},
		{"https://worker.example.com#fragment", rotationTestToken, false},
		{"ftp://worker.example.com", rotationTestToken, false},
		{"https://worker.example.com", "short", false},
		{"https://worker.example.com", rotationTestToken + "\nInjected", false},
		{"", rotationTestToken, false},
	} {
		if err := validateTwoFARotationConfig(AccountTokenGuardConfig{TwoFARotationEndpoint: tt.endpoint, TwoFARotationToken: tt.token}); (err == nil) != tt.valid {
			t.Errorf("configuration %q valid=%v error=%v", tt.endpoint, tt.valid, err)
		}
	}
}

func TestTwoFARotationRequestValidation(t *testing.T) {
	req := AccountTwoFARotationRequest{AccountTokenGuardReloginAccount: AccountTokenGuardReloginAccount{Email: "test@example.com", Password: "p|a,ss", MFASecret: "JBSWY3DPEHPK3PXP"}, RequestID: rotationTestID, Confirmed: true}
	if err := ValidateAccountTwoFARotationRequest(req); err != nil {
		t.Fatal(err)
	}
	req.Confirmed = false
	if ValidateAccountTwoFARotationRequest(req) == nil {
		t.Fatal("missing explicit confirmation accepted")
	}
	req.Confirmed = true
	for _, id := range []string{"", "../result", strings.Repeat("-", 32), "abcdef0123456789abcdef0123456789ab----"} {
		req.RequestID = id
		if ValidateAccountTwoFARotationRequest(req) == nil {
			t.Errorf("invalid id accepted: %q", id)
		}
	}
}

func TestTwoFARotationProtocol(t *testing.T) {
	req := AccountTwoFARotationRequest{AccountTokenGuardReloginAccount: AccountTokenGuardReloginAccount{Email: "TEST@example.com", Password: "p|a,ss", MFASecret: "JBSWY3DPEHPK3PXP"}, RequestID: rotationTestID, Confirmed: true}
	calls := 0
	svc := rotationTestService(t, func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.Header.Get("Authorization") != "Bearer "+rotationTestToken {
			t.Error("missing worker authentication")
		}
		switch r.Method + " " + r.URL.Path {
		case "POST /jobs":
			var received AccountTwoFARotationRequest
			if err := json.NewDecoder(r.Body).Decode(&received); err != nil {
				t.Fatal(err)
			}
			if received != req {
				t.Errorf("request changed: %+v", received)
			}
			w.WriteHeader(http.StatusAccepted)
			_, _ = io.WriteString(w, "{\"id\":\""+rotationTestID+"\",\"email\":\"test@example.com\",\"status\":\"queued\"}")
		case "GET /jobs":
			_, _ = io.WriteString(w, "{\"jobs\":[]}")
		case "POST /jobs/" + rotationTestID + "/verify":
			_, _ = io.WriteString(w, "{\"id\":\""+rotationTestID+"\",\"status\":\"queued\"}")
		case "GET /jobs/" + rotationTestID + "/result":
			_, _ = io.WriteString(w, "{\"id\":\""+rotationTestID+"\",\"email\":\"test@example.com\",\"mfa_secret\":\"NEWSEED\",\"login_verified\":true,\"password\":\"original-password\",\"access_token\":\"must-not-forward\"}")
		default:
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
	})
	if _, err := svc.StartTwoFARotation(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	if jobs, err := svc.TwoFARotationJobs(context.Background()); err != nil || len(jobs) != 0 {
		t.Fatalf("jobs=%v err=%v", jobs, err)
	}
	if _, err := svc.RetryTwoFARotation(context.Background(), rotationTestID); err != nil {
		t.Fatal(err)
	}
	result, err := svc.TwoFARotationResult(context.Background(), rotationTestID)
	if err != nil {
		t.Fatal(err)
	}
	encoded, _ := json.Marshal(result)
	if result.Password != "original-password" || !strings.Contains(string(encoded), "original-password") {
		t.Fatal("verified credential export lost the original password")
	}
	if strings.Contains(string(encoded), "must-not-forward") {
		t.Fatal("forwarded a field outside the credential export contract")
	}
	if calls != 4 {
		t.Fatalf("calls=%d", calls)
	}
}

func TestTwoFARotationRejectsRedirectsAndSanitizesErrors(t *testing.T) {
	leaked := false
	destination := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { leaked = true }))
	defer destination.Close()
	for _, status := range []int{307, 308, 401, 403, 404, 409, 500} {
		svc := rotationTestService(t, func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Location", destination.URL)
			w.WriteHeader(status)
			_, _ = io.WriteString(w, "sensitive-password-and-seed")
		})
		_, err := svc.TwoFARotationJobs(context.Background())
		if err == nil || strings.Contains(err.Error(), "sensitive-password") {
			t.Fatalf("status=%d error=%v", status, err)
		}
	}
	if leaked {
		t.Fatal("followed credential-bearing redirect")
	}
}

func TestTwoFARotationRejectsUnverifiedMismatchedOrOversizedResults(t *testing.T) {
	for _, body := range []string{
		"{\"id\":\"" + rotationTestID + "\",\"email\":\"test@example.com\",\"mfa_secret\":\"SECRET\",\"login_verified\":false}",
		"{\"id\":\"wrong\",\"email\":\"test@example.com\",\"mfa_secret\":\"SECRET\",\"login_verified\":true}",
		"{\"id\":\"" + rotationTestID + "\",\"email\":\"test@example.com\",\"mfa_secret\":\"\",\"login_verified\":true}",
		"invalid-json-sensitive-data", strings.Repeat("x", (2<<20)+1),
	} {
		svc := rotationTestService(t, func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, body) })
		_, err := svc.TwoFARotationResult(context.Background(), rotationTestID)
		if err == nil || strings.Contains(err.Error(), "SECRET") || strings.Contains(err.Error(), "sensitive-data") {
			t.Fatalf("unexpected error: %v", err)
		}
	}
}

func TestTwoFARotationSafeFailureCodes(t *testing.T) {
	for _, code := range []string{"login_access_denied", "login_state_invalid", "rotation_disable_server_error", "rotation_disable_rejected", "sensitive-password-or-token"} {
		status := "login_failed"
		if strings.HasPrefix(code, "rotation_disable_") {
			status = "needs_review"
		}
		svc := rotationTestService(t, func(w http.ResponseWriter, r *http.Request) {
			_ = json.NewEncoder(w).Encode(map[string]any{"jobs": []map[string]any{{
				"id": rotationTestID, "email": "test@example.com", "status": status,
				"error_code": code, "error": "sensitive-upstream-body",
			}}})
		})
		jobs, err := svc.TwoFARotationJobs(context.Background())
		if err != nil || len(jobs) != 1 {
			t.Fatalf("jobs=%v err=%v", jobs, err)
		}
		if code == "sensitive-password-or-token" {
			if jobs[0].ErrorCode != "" {
				t.Fatal("unrecognized diagnostic leaked")
			}
		} else if jobs[0].ErrorCode != code || jobs[0].Status != status {
			t.Fatal("lost safe failure classification")
		}
		encoded, _ := json.Marshal(jobs)
		if strings.Contains(string(encoded), "sensitive") {
			t.Fatal("sensitive worker data leaked")
		}
	}
}

func TestTwoFARotationDeleteHistory(t *testing.T) {
	calls := 0
	svc := rotationTestService(t, func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.Method != http.MethodDelete || r.URL.Path != "/jobs" || r.Header.Get("Authorization") != "Bearer "+rotationTestToken {
			t.Fatal("wrong delete route or auth")
		}
		var req AccountTwoFARotationDeleteRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatal(err)
		}
		if len(req.IDs) != 1 || req.IDs[0] != rotationTestID {
			t.Fatal("incorrect ids")
		}
		_ = json.NewEncoder(w).Encode(AccountTwoFARotationDeleteResult{DeletedIDs: req.IDs})
	})
	for _, ids := range [][]string{nil, {"../private"}, make([]string, 101)} {
		if _, err := svc.DeleteTwoFARotationJobs(context.Background(), AccountTwoFARotationDeleteRequest{IDs: ids}); err == nil {
			t.Fatal("invalid request accepted")
		}
	}
	if calls != 0 {
		t.Fatal("invalid delete forwarded")
	}
	result, err := svc.DeleteTwoFARotationJobs(context.Background(), AccountTwoFARotationDeleteRequest{IDs: []string{rotationTestID}})
	if err != nil || len(result.DeletedIDs) != 1 || result.DeletedIDs[0] != rotationTestID {
		t.Fatalf("delete failed: %v", err)
	}
}

func TestTwoFARotationHistoryMetadataAndFailurePhases(t *testing.T) {
	for _, code := range []string{"login_browser_challenge", "login_email_verification_required", "login_password_rejected", "login_mfa_rejected", "login_upstream_error"} {
		svc := rotationTestService(t, func(w http.ResponseWriter, r *http.Request) {
			_, _ = io.WriteString(w, "{\"jobs\":[{\"id\":\""+rotationTestID+"\",\"status\":\"login_failed\",\"error_code\":\""+code+"\",\"is_latest\":false,\"deletable\":true}]}")
		})
		jobs, err := svc.TwoFARotationJobs(context.Background())
		if err != nil || len(jobs) != 1 || jobs[0].ErrorCode != code || jobs[0].IsLatest == nil || *jobs[0].IsLatest || !jobs[0].Deletable {
			t.Fatalf("history metadata dropped: %v", err)
		}
		logout := AccountSessionLogoutJob{Status: "login_failed", ErrorCode: code}
		sanitizeSessionLogoutJob(&logout)
		if logout.ErrorCode != code {
			t.Fatal("logout phase dropped")
		}
	}
}
