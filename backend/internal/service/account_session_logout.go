package service

import (
	"context"
	"errors"
	"net/http"
	"strings"
)

type AccountSessionLogoutRequest struct {
	AccountTokenGuardReloginAccount
	RequestID string `json:"request_id"`
	Confirmed bool   `json:"confirmed"`
}

type AccountSessionLogoutJob struct {
	Phase          string   `json:"phase,omitempty"`
	PhaseStartedAt *float64 `json:"phase_started_at,omitempty"`
	StartedAt      *float64 `json:"started_at,omitempty"`
	Deletable      bool     `json:"deletable"`

	ID         string   `json:"id"`
	Email      string   `json:"email"`
	Status     string   `json:"status"`
	ErrorCode  string   `json:"error_code,omitempty"`
	CreatedAt  float64  `json:"created_at"`
	FinishedAt *float64 `json:"finished_at,omitempty"`
}

func ValidateAccountSessionLogoutRequest(req AccountSessionLogoutRequest) error {
	if !req.Confirmed {
		return errors.New("请明确确认退出该 ChatGPT 账号的所有会话")
	}
	if !rotationIDPattern.MatchString(req.RequestID) {
		return errors.New("会话退出任务标识不合法")
	}
	return ValidateOpenAITwoFALogin(req.AccountTokenGuardReloginAccount)
}

func sanitizeCredentialPhase(phase string) string {
	switch phase {
	case "login_start", "password", "mfa", "mfa_retry", "workspace", "session", "preflight", "rotating", "logout_preflight", "revoking",
		"verify_login_start", "verify_password", "verify_mfa", "verify_mfa_retry", "verify_workspace", "verify_session", "verify_preflight":
		return phase
	default:
		return ""
	}
}

func sanitizeSessionLogoutJob(job *AccountSessionLogoutJob) {
	job.Phase = sanitizeCredentialPhase(job.Phase)
	switch job.Status {
	case "queued", "logging_in", "revoking":
	default:
		job.Phase = ""
		job.PhaseStartedAt = nil
	}
	switch job.Status {
	case "accepted", "login_failed", "failed", "interrupted":
	default:
		job.Deletable = false
	}

	switch job.Status {
	case "queued", "logging_in", "revoking", "accepted", "login_failed", "failed", "needs_review", "interrupted":
	default:
		job.Status = "needs_review"
	}
	switch job.ErrorCode {
	case "login_failed", "login_password_rejected", "login_mfa_retry_exhausted", "login_mfa_rejected", "login_upstream_error", "login_browser_challenge", "login_email_verification_required", "login_interaction_required", "invalid_credentials", "account_die",
		"logout_control_missing", "logout_rejected", "logout_unconfirmed", "worker_interrupted",
		"login_access_denied", "login_rate_limited", "identity_mismatch", "login_workspace_selection_failed", "login_session_incomplete":
	default:
		job.ErrorCode = ""
	}
}

func (s *AccountTokenGuardService) StartSessionLogout(ctx context.Context, req AccountSessionLogoutRequest) (*AccountSessionLogoutJob, error) {
	if err := ValidateAccountSessionLogoutRequest(req); err != nil {
		return nil, err
	}
	var job AccountSessionLogoutJob
	if err := s.twoFARotationCall(ctx, http.MethodPost, "/session-logout/jobs", req, &job); err != nil {
		var rejected *CredentialSubmissionRejection
		if errors.As(err, &rejected) {
			return nil, rejected
		}
		return nil, errors.New("退出请求未确认；请查询任务并沿用原任务标识，勿重复创建；维护服务忙碌或任务待确认时不会再次执行")
	}
	if !rotationIDPattern.MatchString(job.ID) || !strings.EqualFold(strings.TrimSpace(req.Email), job.Email) {
		return nil, errors.New("维护服务返回了不匹配的退出任务，请查询原任务")
	}
	sanitizeSessionLogoutJob(&job)
	return &job, nil
}

func (s *AccountTokenGuardService) SessionLogoutJobs(ctx context.Context) ([]AccountSessionLogoutJob, error) {
	var response struct {
		Jobs []AccountSessionLogoutJob `json:"jobs"`
	}
	if err := s.twoFARotationCall(ctx, http.MethodGet, "/session-logout/jobs", nil, &response); err != nil {
		return nil, errors.New("无法读取会话退出任务，请检查共用维护服务配置与版本")
	}
	for i := range response.Jobs {
		sanitizeSessionLogoutJob(&response.Jobs[i])
	}
	return response.Jobs, nil
}

func (s *AccountTokenGuardService) DeleteSessionLogoutJobs(ctx context.Context, req AccountTwoFARotationDeleteRequest) (*AccountTwoFARotationDeleteResult, error) {
	if err := ValidateTwoFARotationDelete(req); err != nil {
		return nil, err
	}
	var result AccountTwoFARotationDeleteResult
	if err := s.twoFARotationCall(ctx, http.MethodDelete, "/session-logout/jobs", req, &result); err != nil {
		return nil, err
	}
	return &result, nil
}
