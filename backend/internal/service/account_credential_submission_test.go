package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestTwoFASubmissionRejectionWhitelist(t *testing.T) {
	for _, tc := range []struct {
		path, detail string
		status       int
		rejected     bool
	}{
		{"/jobs", "account_has_unresolved_job", 409, true},
		{"/session-logout/jobs", "account_has_unresolved_logout", 409, true},
		{"/jobs", "credential_worker_busy", 409, true},
		{"/session-logout/jobs", "credential_worker_busy", 409, true},
		{"/jobs", "worker_history_capacity", 409, true},
		{"/session-logout/jobs", "worker_history_capacity", 409, true},
		{"/jobs", "request_id_reused", 409, false},
		{"/jobs", "submission_uncertain_review_jobs", 409, false},
		{"/jobs", "submission_uncertain_review_jobs", 503, false},
		{"/jobs", "private-password-and-seed", 409, false},
		{"/jobs", "account_has_unresolved_logout", 409, false},
		{"/session-logout/jobs", "account_has_unresolved_job", 409, false},
		{"/jobs/" + rotationTestID + "/verify", "credential_worker_busy", 409, false},
		{"/jobs", "account_has_unresolved_job", 500, false},
		{"/jobs", "account_has_unresolved_job", 403, false},
	} {
		t.Run(fmt.Sprintf("%s-%s-%d", tc.path, tc.detail, tc.status), func(t *testing.T) {
			svc := rotationTestService(t, func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tc.status)
				_ = json.NewEncoder(w).Encode(map[string]any{"detail": tc.detail, "password": "private-password", "mfa_secret": "private-seed"})
			})
			var result any
			err := svc.twoFARotationCall(context.Background(), http.MethodPost, tc.path, map[string]string{"request_id": rotationTestID}, &result)
			var rejection *CredentialSubmissionRejection
			if errors.As(err, &rejection) != tc.rejected {
				t.Fatalf("wrong rejection classification: %v", err)
			}
			if tc.rejected && rejection.Reason != tc.detail {
				t.Fatal("lost rejection reason")
			}
			if err == nil || strings.Contains(err.Error(), "private-") {
				t.Fatal("error missing or leaked upstream body")
			}
		})
	}
}

func TestTwoFASubmissionUnparseableConflictStaysUncertain(t *testing.T) {
	for _, body := range []string{"<html>private-cookie</html>", "{", "{\"detail\":{\"secret\":\"private-seed\"}}", "{\"detail\":\"credential_worker_busy private-secret\"}", strings.Repeat("x", (2<<20)+1)} {
		svc := rotationTestService(t, func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(409); _, _ = io.WriteString(w, body) })
		var result any
		err := svc.twoFARotationCall(context.Background(), http.MethodPost, "/jobs", nil, &result)
		var rejection *CredentialSubmissionRejection
		if err == nil || errors.As(err, &rejection) || strings.Contains(err.Error(), "private-") {
			t.Fatal("unsafe malformed conflict handling")
		}
	}
	if credentialSubmissionRejection(http.MethodGet, "/jobs", 409, []byte("{\"detail\":\"credential_worker_busy\"}")) != nil {
		t.Fatal("classified a read as rejected submission")
	}
}

func TestTwoFASubmissionRejectionSurvivesBothStartServices(t *testing.T) {
	svc := rotationTestService(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(409)
		_, _ = io.WriteString(w, "{\"detail\":\"credential_worker_busy\"}")
	})
	entry := AccountTokenGuardReloginAccount{Email: "test@example.com", Password: "synthetic-only", MFASecret: "JBSWY3DPEHPK3PXP"}
	_, rotationErr := svc.StartTwoFARotation(context.Background(), AccountTwoFARotationRequest{AccountTokenGuardReloginAccount: entry, RequestID: rotationTestID, Confirmed: true})
	_, logoutErr := svc.StartSessionLogout(context.Background(), AccountSessionLogoutRequest{AccountTokenGuardReloginAccount: entry, RequestID: rotationTestID, Confirmed: true})
	for _, err := range []error{rotationErr, logoutErr} {
		var rejection *CredentialSubmissionRejection
		if !errors.As(err, &rejection) || rejection.Reason != "credential_worker_busy" {
			t.Fatalf("lost typed error: %v", err)
		}
	}
}
