package service

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"
)

// Rotation is deliberately separate from login/token refresh. No automated
// credential guard cycle is allowed to change an account's second factor.
type AccountTwoFARotationRequest struct {
	AccountTokenGuardReloginAccount
	RequestID string `json:"request_id"`
	Confirmed bool   `json:"confirmed"`
}

type AccountTwoFARotationJob struct {
	ID                   string  `json:"id"`
	Email                string  `json:"email"`
	Status               string  `json:"status"`
	ErrorCode            string  `json:"error_code,omitempty"`
	LoginVerified        bool    `json:"login_verified"`
	RotatedPendingVerify bool    `json:"rotated_pending_verify"`
	Retryable            bool    `json:"retryable"`
	CreatedAt            float64 `json:"created_at"`
}

type AccountTwoFARotationResult struct {
	ID            string `json:"id"`
	Email         string `json:"email"`
	Password      string `json:"password"`
	MFASecret     string `json:"mfa_secret"`
	LoginVerified bool   `json:"login_verified"`
}

// Only fixed diagnostic codes cross the admin API; never forward upstream text.
func sanitizeTwoFARotationJob(job *AccountTwoFARotationJob) {
	switch job.ErrorCode {
	case "login_access_denied", "login_rate_limited", "login_bootstrap_rejected",
		"login_interaction_required", "login_state_invalid", "invalid_credentials",
		"account_die", "login_failed", "preflight_failed":
	default:
		job.ErrorCode = ""
	}
}

var rotationIDPattern = regexp.MustCompile(`^(?:[a-fA-F0-9]{32}|[a-fA-F0-9]{8}-[a-fA-F0-9]{4}-[a-fA-F0-9]{4}-[a-fA-F0-9]{4}-[a-fA-F0-9]{12})$`)

func validateTwoFARotationConfig(c AccountTokenGuardConfig) error {
	endpoint, token := strings.TrimSpace(c.TwoFARotationEndpoint), strings.TrimSpace(c.TwoFARotationToken)
	if endpoint == "" && token == "" {
		return nil
	}
	u, err := url.Parse(endpoint)
	if err != nil || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Scheme != "http" && u.Scheme != "https") {
		return errors.New("2FA 维护服务需要不含用户信息、查询参数或片段的 http/https 地址")
	}
	host := strings.ToLower(u.Hostname())
	ip := net.ParseIP(host)
	private := host == "localhost" || (!strings.ContainsAny(host, ".:") && ip == nil) || (ip != nil && (ip.IsLoopback() || ip.IsPrivate()))
	if u.Scheme == "http" && !private {
		return errors.New("公网 2FA 维护服务必须使用 HTTPS")
	}
	if len(token) < 32 || len(token) > 4096 || strings.ContainsAny(token, "\r\n") {
		return errors.New("2FA 维护服务密钥需要 32–4096 个字符且不能包含换行")
	}
	return nil
}

func ValidateAccountTwoFARotationRequest(req AccountTwoFARotationRequest) error {
	if !req.Confirmed {
		return errors.New("请明确确认更换账号 2FA")
	}
	if !rotationIDPattern.MatchString(req.RequestID) {
		return errors.New("2FA 任务幂等标识不合法")
	}
	return ValidateOpenAITwoFALogin(req.AccountTokenGuardReloginAccount)
}

func (s *AccountTokenGuardService) twoFARotationCall(ctx context.Context, method, path string, payload, result any) error {
	cfg := s.currentConfig()
	if strings.TrimSpace(cfg.TwoFARotationEndpoint) == "" {
		return errors.New("请先配置自托管 2FA 维护服务")
	}
	if err := validateTwoFARotationConfig(cfg); err != nil {
		return err
	}
	var body io.Reader
	if payload != nil {
		encoded, err := json.Marshal(payload)
		if err != nil {
			return errors.New("2FA 请求编码失败")
		}
		body = bytes.NewReader(encoded)
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, method, strings.TrimRight(cfg.TwoFARotationEndpoint, "/")+path, body)
	if err != nil {
		return errors.New("2FA 请求构造失败")
	}
	req.Header.Set("Authorization", "Bearer "+strings.TrimSpace(cfg.TwoFARotationToken))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	client := http.Client{}
	if s.httpClient != nil {
		client = *s.httpClient
	}
	// Even 307/308 must not forward passwords or the worker token elsewhere.
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	resp, err := client.Do(req)
	if err != nil {
		return errors.New("2FA 服务通信未确认，请保留任务标识并查询原任务，勿重复创建")
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, (2<<20)+1))
	if err != nil || len(data) > 2<<20 {
		return errors.New("2FA 服务响应读取失败")
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		// Never include upstream bodies/errors: they may echo credentials.
		switch resp.StatusCode {
		case http.StatusUnauthorized, http.StatusForbidden:
			return errors.New("2FA 维护服务鉴权失败")
		case http.StatusConflict:
			return errors.New("2FA 任务冲突或状态待确认，请查询原任务；仅已更换待验证任务可继续验证")
		case http.StatusNotFound:
			return errors.New("2FA 任务不存在")
		default:
			return errors.New("2FA 维护服务拒绝请求，请检查服务配置与任务状态")
		}
	}
	if err := json.Unmarshal(data, result); err != nil {
		return errors.New("2FA 服务响应格式不正确")
	}
	return nil
}

func (s *AccountTokenGuardService) StartTwoFARotation(ctx context.Context, req AccountTwoFARotationRequest) (*AccountTwoFARotationJob, error) {
	if err := ValidateAccountTwoFARotationRequest(req); err != nil {
		return nil, err
	}
	var job AccountTwoFARotationJob
	if err := s.twoFARotationCall(ctx, http.MethodPost, "/jobs", req, &job); err != nil {
		return nil, err
	}
	if !rotationIDPattern.MatchString(job.ID) || !strings.EqualFold(strings.TrimSpace(req.Email), job.Email) {
		return nil, errors.New("2FA 服务返回了不匹配的任务，请查询原任务")
	}
	sanitizeTwoFARotationJob(&job)
	return &job, nil
}

func (s *AccountTokenGuardService) TwoFARotationJobs(ctx context.Context) ([]AccountTwoFARotationJob, error) {
	var response struct {
		Jobs []AccountTwoFARotationJob `json:"jobs"`
	}
	if err := s.twoFARotationCall(ctx, http.MethodGet, "/jobs", nil, &response); err != nil {
		return nil, err
	}
	for i := range response.Jobs {
		sanitizeTwoFARotationJob(&response.Jobs[i])
	}
	return response.Jobs, nil
}

func (s *AccountTokenGuardService) RetryTwoFARotation(ctx context.Context, id string) (*AccountTwoFARotationJob, error) {
	if !rotationIDPattern.MatchString(id) {
		return nil, errors.New("2FA 任务标识不合法")
	}
	var job AccountTwoFARotationJob
	if err := s.twoFARotationCall(ctx, http.MethodPost, "/jobs/"+id+"/verify", nil, &job); err != nil {
		return nil, err
	}
	if job.ID != id {
		return nil, errors.New("2FA 服务返回了不匹配的任务")
	}
	sanitizeTwoFARotationJob(&job)
	return &job, nil
}

func (s *AccountTokenGuardService) TwoFARotationResult(ctx context.Context, id string) (*AccountTwoFARotationResult, error) {
	if !rotationIDPattern.MatchString(id) {
		return nil, errors.New("2FA 任务标识不合法")
	}
	var result AccountTwoFARotationResult
	if err := s.twoFARotationCall(ctx, http.MethodGet, "/jobs/"+id+"/result", nil, &result); err != nil {
		return nil, err
	}
	if result.ID != id || !result.LoginVerified || result.Email == "" || strings.TrimSpace(result.MFASecret) == "" {
		return nil, errors.New("新 2FA 尚未验证成功，不能导出或回写")
	}
	return &result, nil
}
