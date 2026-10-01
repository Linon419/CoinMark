package service

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/jmoiron/sqlx"

	"coinmark/api-go/internal/repo/sqlite"
)

// 美国现货加密 ETF 每日资金流，数据来自 SoSoValue（按币汇总全部 ETF）：
//   - OpenAPI：只支持 BTC/ETH/SOL/XRP/LTC/HBAR/DOGE/LINK/AVAX/DOT，每次只返回最近约 1 个月；
//   - 其余币（HYPE/ZEC/BNB/TRX/NEAR）只在网页上有，网页有 Cloudflare 验证，经 FlareSolverr 打开后
//     从 Next.js 页面数据 __NEXT_DATA__ 读取完整历史。

const (
	sosoValueSummaryURL = "https://openapi.sosovalue.com/openapi/v1/etfs/summary-history"
	sosoValuePageURL    = "https://sosovalue.com/assets/etf/us-%s-spot"
	// SoSoValue 免费额度约每分钟 10 次，逐个币请求时留足间隔
	sosoValueRequestGap = 7 * time.Second
)

type EtfFlowRow struct {
	Asset        string  `db:"asset" json:"asset"`
	Date         string  `db:"date" json:"date"` // YYYY-MM-DD（美股交易日）
	NetInflow    float64 `db:"net_inflow" json:"netInflow"`
	ValueTraded  float64 `db:"value_traded" json:"valueTraded"`
	NetAssets    float64 `db:"net_assets" json:"netAssets"`
	CumNetInflow float64 `db:"cum_net_inflow" json:"cumNetInflow"`
}

type EtfFlowSummary struct {
	Asset  string       `json:"asset"`
	Latest EtfFlowRow   `json:"latest"`
	Sum5   float64      `json:"sum5"`  // 最近 5 个交易日净流入合计
	Sum20  float64      `json:"sum20"` // 最近 20 个交易日净流入合计
	Series []EtfFlowRow `json:"series"`
}

func parseSoSoSummary(body []byte, asset string) ([]EtfFlowRow, error) {
	var resp struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
		Data    []struct {
			Date             string  `json:"date"`
			TotalNetInflow   float64 `json:"total_net_inflow"`
			TotalValueTraded float64 `json:"total_value_traded"`
			TotalNetAssets   float64 `json:"total_net_assets"`
			CumNetInflow     float64 `json:"cum_net_inflow"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, err
	}
	if resp.Code != 0 {
		return nil, fmt.Errorf("sosovalue %s: code=%d %s", asset, resp.Code, resp.Message)
	}
	rows := make([]EtfFlowRow, 0, len(resp.Data))
	for _, d := range resp.Data {
		rows = append(rows, EtfFlowRow{Asset: asset, Date: d.Date, NetInflow: d.TotalNetInflow,
			ValueTraded: d.TotalValueTraded, NetAssets: d.TotalNetAssets, CumNetInflow: d.CumNetInflow})
	}
	return rows, nil
}

func fetchSoSoSummary(ctx context.Context, client *http.Client, apiKey, asset string) ([]EtfFlowRow, error) {
	q := url.Values{"symbol": {asset}, "country_code": {"US"}, "limit": {"300"}}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, sosoValueSummaryURL+"?"+q.Encode(), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("x-soso-api-key", apiKey)
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("sosovalue %s: http %d %.120s", asset, resp.StatusCode, body)
	}
	return parseSoSoSummary(body, asset)
}

func parseSoSoPage(html, asset string) ([]EtfFlowRow, error) {
	const startTag = `<script id="__NEXT_DATA__" type="application/json">`
	i := strings.Index(html, startTag)
	if i < 0 {
		return nil, fmt.Errorf("sosovalue page %s: no __NEXT_DATA__", asset)
	}
	rest := html[i+len(startTag):]
	j := strings.Index(rest, "</script>")
	if j < 0 {
		return nil, fmt.Errorf("sosovalue page %s: unterminated __NEXT_DATA__", asset)
	}
	var data struct {
		Props struct {
			PageProps struct {
				HistoryData struct {
					List []struct {
						DataDate       string  `json:"dataDate"`
						TotalNetInflow float64 `json:"totalNetInflow"`
						TotalVolume    float64 `json:"totalVolume"`
						TotalNetAssets float64 `json:"totalNetAssets"`
						CumNetInflow   float64 `json:"cumNetInflow"`
					} `json:"list"`
				} `json:"historyData"`
			} `json:"pageProps"`
		} `json:"props"`
	}
	if err := json.Unmarshal([]byte(rest[:j]), &data); err != nil {
		return nil, fmt.Errorf("sosovalue page %s: %w", asset, err)
	}
	list := data.Props.PageProps.HistoryData.List
	if len(list) == 0 {
		return nil, fmt.Errorf("sosovalue page %s: empty history", asset)
	}
	rows := make([]EtfFlowRow, 0, len(list))
	for _, d := range list {
		if len(d.DataDate) < 10 {
			continue
		}
		rows = append(rows, EtfFlowRow{Asset: asset, Date: d.DataDate[:10], NetInflow: d.TotalNetInflow,
			ValueTraded: d.TotalVolume, NetAssets: d.TotalNetAssets, CumNetInflow: d.CumNetInflow})
	}
	return rows, nil
}

func parseFlareSolverr(body []byte) (string, error) {
	var resp struct {
		Status   string `json:"status"`
		Message  string `json:"message"`
		Solution struct {
			Status   int    `json:"status"`
			Response string `json:"response"`
		} `json:"solution"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return "", err
	}
	if resp.Status != "ok" {
		return "", fmt.Errorf("flaresolverr: %s %s", resp.Status, resp.Message)
	}
	if resp.Solution.Status != http.StatusOK {
		return "", fmt.Errorf("flaresolverr: target http %d", resp.Solution.Status)
	}
	return resp.Solution.Response, nil
}

func fetchSoSoPage(ctx context.Context, client *http.Client, flareSolverrURL, asset string) ([]EtfFlowRow, error) {
	payload, _ := json.Marshal(map[string]interface{}{
		"cmd": "request.get", "url": fmt.Sprintf(sosoValuePageURL, strings.ToLower(asset)), "maxTimeout": 60000,
	})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, flareSolverrURL, strings.NewReader(string(payload)))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	html, err := parseFlareSolverr(body)
	if err != nil {
		return nil, fmt.Errorf("sosovalue page %s: %w", asset, err)
	}
	return parseSoSoPage(html, asset)
}

func UpsertEtfFlows(ctx context.Context, store *sqlite.Store, rows []EtfFlowRow) error {
	if len(rows) == 0 {
		return nil
	}
	nowMs := time.Now().UnixMilli()
	return store.Write(ctx, func(_ context.Context, tx *sqlx.Tx) error {
		for _, r := range rows {
			if _, err := tx.Exec(`INSERT INTO etf_flows (asset, date, net_inflow, value_traded, net_assets, cum_net_inflow, updated_ms)
VALUES (?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(asset, date) DO UPDATE SET net_inflow = excluded.net_inflow, value_traded = excluded.value_traded,
	net_assets = excluded.net_assets, cum_net_inflow = excluded.cum_net_inflow, updated_ms = excluded.updated_ms`,
				r.Asset, r.Date, r.NetInflow, r.ValueTraded, r.NetAssets, r.CumNetInflow, nowMs); err != nil {
				return err
			}
		}
		return nil
	})
}

func ListEtfFlowRows(ctx context.Context, store *sqlite.Store, sinceDate string) ([]EtfFlowRow, error) {
	var rows []EtfFlowRow
	err := store.SelectContext(ctx, &rows, `SELECT asset, date, net_inflow, value_traded, net_assets, cum_net_inflow
FROM etf_flows WHERE date >= ? ORDER BY asset, date`, sinceDate)
	return rows, err
}

// summarizeEtfFlows 按币汇总，序列只保留最近 days 个交易日（升序），按最新一日净流入从高到低排序。
func summarizeEtfFlows(rows []EtfFlowRow, days int) []EtfFlowSummary {
	byAsset := map[string][]EtfFlowRow{}
	for _, r := range rows {
		byAsset[r.Asset] = append(byAsset[r.Asset], r)
	}
	out := make([]EtfFlowSummary, 0, len(byAsset))
	for asset, rs := range byAsset {
		sort.Slice(rs, func(i, j int) bool { return rs[i].Date < rs[j].Date })
		s := EtfFlowSummary{Asset: asset, Latest: rs[len(rs)-1]}
		for i := len(rs) - 1; i >= 0 && i >= len(rs)-20; i-- {
			if i >= len(rs)-5 {
				s.Sum5 += rs[i].NetInflow
			}
			s.Sum20 += rs[i].NetInflow
		}
		if days > 0 && len(rs) > days {
			rs = rs[len(rs)-days:]
		}
		s.Series = rs
		out = append(out, s)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Latest.NetInflow > out[j].Latest.NetInflow })
	return out
}

// GetEtfFlowSummaries 读取最近约 60 天的数据并按币汇总。
func GetEtfFlowSummaries(ctx context.Context, store *sqlite.Store, days int) ([]EtfFlowSummary, error) {
	rows, err := ListEtfFlowRows(ctx, store, time.Now().UTC().AddDate(0, 0, -60).Format("2006-01-02"))
	if err != nil {
		return nil, err
	}
	return summarizeEtfFlows(rows, days), nil
}

// RunEtfFlowSync 启动时和之后每小时同步一次：apiAssets 走 OpenAPI，pageAssets 经 FlareSolverr 读网页
// （flareSolverrURL 为空则跳过）。单个币失败只记日志，不影响其他币。
func RunEtfFlowSync(ctx context.Context, store *sqlite.Store, apiKey string, apiAssets []string, flareSolverrURL string, pageAssets []string, stopCh <-chan struct{}) {
	apiClient := &http.Client{Timeout: 20 * time.Second}
	pageClient := &http.Client{Timeout: 90 * time.Second} // FlareSolverr 需要启动浏览器过验证
	type job struct {
		asset string
		fetch func(string) ([]EtfFlowRow, error)
	}
	var jobs []job
	for _, a := range apiAssets {
		jobs = append(jobs, job{a, func(asset string) ([]EtfFlowRow, error) { return fetchSoSoSummary(ctx, apiClient, apiKey, asset) }})
	}
	if flareSolverrURL != "" {
		for _, a := range pageAssets {
			jobs = append(jobs, job{a, func(asset string) ([]EtfFlowRow, error) {
				return fetchSoSoPage(ctx, pageClient, flareSolverrURL, asset)
			}})
		}
	}
	ticker := time.NewTicker(time.Hour)
	defer ticker.Stop()
	for {
		ok := 0
		for i, j := range jobs {
			if i > 0 {
				select {
				case <-stopCh:
					return
				case <-ctx.Done():
					return
				case <-time.After(sosoValueRequestGap):
				}
			}
			asset := strings.ToUpper(strings.TrimSpace(j.asset))
			if asset == "" {
				continue
			}
			rows, err := j.fetch(asset)
			if err == nil {
				err = UpsertEtfFlows(ctx, store, rows)
			}
			if err != nil {
				log.Printf("etf flows: %v", err)
				continue
			}
			ok++
		}
		log.Printf("etf flows: synced %d/%d assets", ok, len(jobs))
		select {
		case <-stopCh:
			return
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}
