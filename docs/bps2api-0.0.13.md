# bps2api 0.0.13

Excel / BPS 开关只决定 OpenAI OAuth 账号的上游通道。Anthropic、Gemini、Grok、
DeepSeek 等其他供应商保持各自的路由；使用 OpenAI 兼容请求格式不代表供应商是 OpenAI。
客户端仍使用网关的
`/v1/responses`、`/v1/chat/completions`、`/v1/messages` 等接入地址；选中已开启
BPS 的 OpenAI OAuth 账号后，上游 Responses
地址固定为 `https://bps.openai.com/basispoints/api/responses`。

## 路由修复

- 移除根据工具、历史和可选参数自动改走原生 Codex 的分支。
- 该 OpenAI 账号已保存的 `openai_excel_bps_models` 仅作为旧配置保留，不再让列表外模型绕过 BPS。
- 上游 403 导致暂停时保留开关和通道选择，返回 `basispoints_routing_paused`；管理员检查权限并恢复后继续使用 BPS。
- 本地参数校验前记录 BPS 端点，避免 BPS 校验错误被标成默认的 `/v1/responses`。
- BPS 不支持的独立 Images API 和 alpha search 接口返回 `basispoints_endpoint_unsupported`，不会悄悄发送给原生服务。

## 图片兼容

在语义明确、没有冲突或额外二进制数据的前提下，统一处理 `image`、`image_url`、
`input_image`、`output_image`、`screenshot`、`computer_screenshot` 和 `provider_image`
中的 `image_url` / `source` / `url` 引用。内嵌 data URL 仍经过原有有界图片中继，
容量检查与转换使用同一组引用形态。`detail=original` 原样保留，不降低图片精度。

互相冲突的引用、非字符串 detail、音频或文件内容不会伪装成已被模型读取的图片。
不支持的请求返回可诊断错误；此版本不承诺上游权限、限流、模型不可用等错误消失。

## 升级

没有数据库迁移。关闭 BPS 后使用原生通道；开启 BPS 的账号请确认具备上游模型权限。
先验证文本、工具往返、原始精度图片和 SSE，再切换生产流量。滚动部署期间保留旧实例
的临时图片下载路径，避免仍在处理的请求丢失图片。
