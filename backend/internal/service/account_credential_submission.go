package service

import (
	"encoding/json"
	"net/http"
)

// CredentialSubmissionRejection means the worker explicitly refused this new
// request before reserving its ID. Unknown errors must retain the original ID.
type CredentialSubmissionRejection struct {
	Reason string
}

func (e *CredentialSubmissionRejection) Error() string {
	switch e.Reason {
	case "account_has_unresolved_job":
		return "本次未创建任务：该账号已有进行中或结果待确认的 2FA 任务，请查看已有任务"
	case "account_has_unresolved_logout":
		return "本次未创建任务：该账号已有结果待确认的退出任务，请查看已有任务"
	case "credential_worker_busy":
		return "本次未创建任务：维护服务正在处理其他任务，请等待完成后再提交"
	case "worker_history_capacity":
		return "本次未创建任务：维护服务任务记录已达上限，请联系管理员处理"
	default:
		return "本次请求未被接收"
	}
}

func credentialSubmissionRejection(method, path string, status int, body []byte) error {
	if method != http.MethodPost || status != http.StatusConflict || (path != "/jobs" && path != "/session-logout/jobs") {
		return nil
	}
	var result struct {
		Detail string `json:"detail"`
	}
	if json.Unmarshal(body, &result) != nil {
		return nil
	}
	// These exact worker branches run before recording or dispatching a request.
	// request_id_reused and submission_uncertain_review_jobs are NOT rejections.
	switch result.Detail {
	case "credential_worker_busy", "worker_history_capacity":
	case "account_has_unresolved_job":
		if path != "/jobs" {
			return nil
		}
	case "account_has_unresolved_logout":
		if path != "/session-logout/jobs" {
			return nil
		}
	default:
		return nil
	}
	return &CredentialSubmissionRejection{Reason: result.Detail}
}
