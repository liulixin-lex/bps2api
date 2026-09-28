# BPS2API 0.0.15

修复 0.0.14 的工具兼容回归：请求声明可选 web_search、image_generation、tool_search 等工具时，不再因缺少账号省略开关而拒绝整个请求。参照 ranxi2001/sub2api 最新 production 实现，保留客户端 function/custom/namespace 工具执行链路和能力提示。

- required、指定客户端工具、allowed_tools、原始参数、工具结果续接与缓存目录继续有效。
- 新旧账号自动兼容，无需数据库迁移或批量修改配置；账号界面显示自动兼容说明。
- X-BPS-Omitted-Hosted-Tools 响应头提供明确诊断。省略声明不等于执行托管搜索/图片生成；不会虚构工具结果或静默换用原生通道。

本次是工具兼容修复，不改变上游账号认证或模型权限。保留旧容器和图片下载路径以支持回滚。
