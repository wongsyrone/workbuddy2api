package server

import (
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"strings"
	"time"

	"workbuddy2api/internal/logfmt"
	"workbuddy2api/internal/pool"
	"workbuddy2api/internal/upstream"
)

// ---------------------------------------------------------------------------
// /v1/tools/search —— agenttool web search 透传（账号池轮转 + 15001 配额冷却）
// ---------------------------------------------------------------------------

// toolSearchRequest /v1/tools/search 入站请求体。
type toolSearchRequest struct {
	Query          string   `json:"query"`
	Type           string   `json:"type"`
	MaxResults     int      `json:"max_results"`
	AllowedDomains []string `json:"allowed_domains"`
	BlockedDomains []string `json:"blocked_domains"`
	Freshness      string   `json:"freshness"`
}

// 搜索日配额暂按固定十分钟软冷却避让，不禁用账号或叠加熔断。
const toolSearchQuotaCooldown = 10 * time.Minute

// toolSearch handler：POST /v1/tools/search
//
// 选号语义（与 chatCompletions 的关键差异）：
//   - **不走会话粘性**：搜索无上下文，粘住单号会把该号的 15001 搜索日配额打穿。
//     每次请求在当前密钥可见的业务分组内轮换，不设模型限制。
//   - 15001（quota_web_search）→ 账号软冷却 + 换号重试；全部号配额耗尽 → 429 透传。
//   - CN/global 均支持，按选中账号的 realm 决定上游域名。
func (h *Handler) toolSearch(w http.ResponseWriter, r *http.Request) {
	start := time.Now()
	var req toolSearchRequest
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20))
	err := decoder.Decode(&req)
	if err == nil {
		err = decoder.Decode(&struct{}{})
		if err == io.EOF {
			err = nil
		} else if err == nil {
			err = errors.New("request body must contain one JSON object")
		}
	}
	if err != nil {
		status := http.StatusBadRequest
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			status = http.StatusRequestEntityTooLarge
		}
		writeJSON(w, status, map[string]any{
			"error": "invalid request body: " + err.Error(),
		})
		return
	}
	req.Query = strings.TrimSpace(req.Query)
	if req.Query == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{
			"error": "query is required",
		})
		return
	}
	if req.Type != "" && req.Type != "text2text" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "type must be text2text"})
		return
	}
	if req.MaxResults <= 0 {
		req.MaxResults = 5
	}
	if req.MaxResults > 20 {
		req.MaxResults = 20
	}
	switch req.Freshness {
	case "", "d1", "m1", "y1":
	default:
		writeJSON(w, http.StatusBadRequest, map[string]any{
			"error": "freshness must be one of: d1, m1, y1",
		})
		return
	}

	// blocked_domains：官方 CLI 语义是拼进 query 文本（" -site:domain"）。
	// 在网关层完成拼接，调用方（DSH 插件）无需自行处理。
	q := req.Query
	for _, d := range req.BlockedDomains {
		d = strings.TrimSpace(d)
		if d != "" {
			q += " -site:" + d
		}
	}

	upReq := upstream.AgentToolSearchRequest{
		Query:          q,
		Type:           "text2text",
		MaxResults:     req.MaxResults,
		AllowedDomains: req.AllowedDomains,
		Freshness:      req.Freshness,
	}

	tried := map[string]bool{}
	var lastBody string
	var lastStatus int
	quotaExhausted := 0 // 15001 计数：全部号配额耗尽时末端语义化
	var groups []string
	if key := authKeyFrom(r.Context()); key != nil {
		groups = key.Groups
	}

	for i := 0; i < h.cfg.MaxRotate; i++ {
		if r.Context().Err() != nil {
			return
		}
		if i > 0 && !rotateBackoff(i-1, r.Context()) {
			return
		}
		acct := h.cfg.Pool.PickExcludingForRealmGroups(tried, "", "", groups)
		if acct == nil {
			break
		}
		// realm 由账号本身决定（agentToolSearchBase 分域），CN/global 都可搜。
		tried[acct.UID] = true
		if !h.cfg.Pool.Acquire(acct.UID) {
			continue // 在途满：换号
		}
		// 搜索也可能是启动后的首个请求，沿用聊天的续期、观测和原子落盘规则。
		if acct.NeedsRefresh(h.cfg.RefreshSkew) {
			if err := h.cfg.Upstream.RefreshToken(acct); err != nil {
				h.cfg.Pool.Release(acct.UID)
				if r.Context().Err() != nil {
					return
				}
				lastStatus, lastBody = http.StatusServiceUnavailable, err.Error()
				h.cfg.Pool.NoteRefreshFail(acct.UID, err)
				var ue *upstream.Error
				if errors.As(err, &ue) && ue.Kind == upstream.ErrSessionDead {
					h.cfg.Pool.NoteSessionDead(acct.UID)
				} else {
					h.cfg.Pool.NoteError(acct.UID)
				}
				continue
			}
			h.cfg.Pool.NoteRefreshOK(acct.UID)
			acct.BackfillRealm()
			if err := acct.SaveAtomic(); err != nil {
				log.Printf("ERR: [server] search refresh acct=%s: save auth failed: %v", logfmt.Label(acct.UID, acct.Nickname), err)
			}
		}
		if r.Context().Err() != nil {
			h.cfg.Pool.Release(acct.UID)
			return
		}

		resp, err := h.cfg.Upstream.AgentToolSearch(r.Context(), acct, upReq)
		h.cfg.Pool.Release(acct.UID)
		if r.Context().Err() != nil {
			return
		}

		var uerr *upstream.Error
		if errors.As(err, &uerr) {
			lastStatus = uerr.Status
			lastBody = uerr.Msg
			switch uerr.Kind {
			case upstream.ErrSoftRate, upstream.ErrHardCredit, upstream.ErrAccountFault, upstream.ErrWafBlock,
				upstream.ErrServer, upstream.ErrNotFound:
				// 账号级可轮转故障：走既有错误策略（冷却/计数）+ 换号。
				h.applyErrorPolicy(acct.UID, uerr.Kind, uerr.Msg, "", uerr)
				continue
			case upstream.ErrSessionDead, upstream.ErrAuthRejected:
				h.applyErrorPolicy(acct.UID, uerr.Kind, uerr.Msg, "", uerr)
				continue
			case upstream.ErrUpstreamQueue:
				writeJSON(w, http.StatusTooManyRequests, map[string]any{
					"error": "upstream search is queueing",
					"msg":   uerr.Msg,
				})
				return
			default:
				// ErrClient / ErrBadParams / 其他：请求问题，不罚号不轮转，透传。
				writeJSON(w, http.StatusBadRequest, map[string]any{
					"error": "search request rejected by upstream",
					"code":  int(uerr.Kind),
					"msg":   uerr.Msg,
				})
				log.Printf("WARN: [server] search client error acct=%s: %s", logfmt.Label(acct.UID, acct.Nickname), uerr.Msg)
				return
			}
		}
		if err != nil {
			// 非分类错误（marshal 等本地问题）：透传 500。
			writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
			return
		}

		// 上游 200 但业务码非 0（搜索域的业务信封，如 15001）。
		if resp.Code != 0 {
			lastBody = resp.Msg
			lastStatus = http.StatusTooManyRequests
			if resp.IsQuotaExhausted() {
				quotaExhausted++
				// 15001 是账号级搜索日配额：软冷却该号（不熔断不禁用），换号重试。
				// 固定时长软冷却（不走 CooldownSoftRate 的 streak 退避：搜索配额
				// 与请求频率无关，固定 10 分钟即语义；恢复由上游日重置或冷却到期）。
				h.cfg.Pool.Cooldown(acct.UID, pool.CoolSoft, toolSearchQuotaCooldown, "15001 web search quota")
				log.Printf("INFO: [server] search quota exhausted acct=%s, rotating", logfmt.Label(acct.UID, acct.Nickname))
				continue
			}
			// 其他业务码：原样透传（与 chat 的 error-passthrough 同哲学）。
			writeJSON(w, http.StatusOK, resp)
			return
		}

		// 成功：原样透传上游信封（results/total_results/response_time_ms/provider）。
		h.cfg.Pool.NoteSuccess(acct.UID)
		log.Printf("INFO: [server] search ok acct=%s results=%d ms=%d total=%s",
			logfmt.Label(acct.UID, acct.Nickname), len(resp.Results), resp.ResponseTimeMs,
			time.Since(start).Round(time.Millisecond))
		writeJSON(w, http.StatusOK, resp)
		return
	}

	// 轮转结束仍无成功。
	if quotaExhausted > 0 && quotaExhausted == len(tried) {
		// 全池搜索配额耗尽：429 + 语义化提示（DSH 插件按此提示用户）。
		writeJSON(w, http.StatusTooManyRequests, map[string]any{
			"error":  "web search quota exhausted on all attempted accounts (upstream code 15001); quota resets daily",
			"code":   "quota_web_search",
			"tried":  quotaExhausted,
			"detail": lastBody,
		})
		return
	}
	status := http.StatusServiceUnavailable
	if lastStatus >= 400 {
		status = lastStatus
	}
	writeJSON(w, status, map[string]any{
		"error":  "no account available for web search",
		"detail": lastBody,
	})
}
