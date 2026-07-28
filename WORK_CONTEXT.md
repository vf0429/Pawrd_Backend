# WORK_CONTEXT — Pawrd_Backend

**TASK_ID:** TASK-20260602-1705
**更新时间:** 2026-06-02 17:05
**完整记录:** [→ 根目录 master_progress.md](../../master_progress.md)

## 当前任务
收口 insurance 专用 Go RAG 链路：已加入 model-based pre-router（direct_response / rag_query / out_of_scope），并继续排查 `/api/chat` 在 insurance 模式下退化到 `127.0.0.1:8012` HTTP fallback 的链路问题。

## 状态速览
- ✅ 已完成：insurance Go RAG 删除 deterministic compare gate，改为 model-based routing / summarization judgment
- ✅ 已完成：加入 insurance pre-router，模型判断 `direct_response`、`rag_query`、`out_of_scope`
- ✅ 已完成：`Hi`、`What can you do?` 在 `/api/rag/go/query` 上已验证返回 `direct_response`
- ✅ 已完成：提交并推送后端 commit `feaa8c0`
- ✅ 已确认：medical 通过 `/api/chat` 返回 `only insurance model is currently supported`，这条拒绝是正确的
- ✅ 已定位：insurance `/api/chat` 的 `127.0.0.1:8012` 报错来自 `internal/handlers/chat_proxy.go` 的静默 HTTP fallback，不是最终回答逻辑本身
- ✅ 已定位：`GO_RAG_INPROCESS_ENABLED` 默认并未关闭；问题是 in-process 任意失败都会被吞掉，然后退化到 `GO_RAG_BASE_URL`（默认 `http://127.0.0.1:8012/api/rag/go`）
- ✅ 已修复：`go` runtime 下启用 in-process 时，不再因为 in-process 失败静默回退到 `8012` loopback HTTP fallback；现在会直接返回明确的 in-process 错误
- ✅ 已补测试：锁定 `/api/chat` 默认走 in-process，以及 in-process 失败时不再伪装成 8012 connection refused
- ⏳ 待处理：把 insurance 与 medical 的整体调用链彻底隔离，防止前端模式与后端链路漂移

## 本次涉及的关键文件
- `internal/services/raggo/runtime.go` — insurance Go RAG pre-router、LLM summary、direct_response / out_of_scope 分流
- `internal/services/raggo/runtime_test.go` — router 与 summary 测试
- `internal/services/raggo/corpus.go` — chunk cache、topic tag 和 retrieval 质量优化
- `internal/handlers/chat_proxy.go` — `/api/chat` insurance 代理；当前需要继续排查这里为何退化到 `queryInsuranceViaHTTP`

## 本次关键结论
- medical 和 insurance 在后端 `/api/chat` 语义上并未混用：medical 当前被明确拒绝，insurance 才允许进入 insurance chat proxy
- 当前用户截图中的 insurance 报错不是回答逻辑问题，而是服务连接问题
- 报错的直接原因是 chat proxy 在请求 `http://127.0.0.1:8012/api/rag/go/query`，但该地址没有服务监听，导致 `connection refused`
- 真正根因不是 `GO_RAG_INPROCESS_ENABLED` 被关掉；默认逻辑里它是开启的
- 真正根因是 `goRAGClient.queryInsurance()` 之前会在 `queryInsuranceInProcess()` 失败时静默吞错，并无条件回退到 `queryInsuranceViaHTTP()`
- `queryInsuranceViaHTTP()` 使用的地址来自配置默认值 `GO_RAG_BASE_URL=http://127.0.0.1:8012/api/rag/go`
- 这会把真实的 in-process 失败伪装成“8012 服务不可用”，导致排查方向漂移
