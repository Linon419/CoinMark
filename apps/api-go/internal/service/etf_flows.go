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

// 美国现货加密 ETF 每日资金流，数据来自 SoSoValue OpenAPI（按币汇总全部 ETF）。
// 接口每次只返回最近约 1 个月，按天落库后历史会逐渐积累。

const (
	sosoValueSummaryURL = "https://openapi.sosovalue.com/openapi/v1/etfs/summary-history"
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

// RunEtfFlowSync 启动时和之后每小时同步一次。单个币失败只记日志，不影响其他币。
func RunEtfFlowSync(ctx context.Context, store *sqlite.Store, apiKey string, assets []string, stopCh <-chan struct{}) {
	client := &http.Client{Timeout: 20 * time.Second}
	ticker := time.NewTicker(time.Hour)
	defer ticker.Stop()
	for {
		ok := 0
		for i, a := range assets {
			if i > 0 {
				select {
				case <-stopCh:
					return
				case <-ctx.Done():
					return
				case <-time.After(sosoValueRequestGap):
				}
			}
			asset := strings.ToUpper(strings.TrimSpace(a))
			rows, err := fetchSoSoSummary(ctx, client, apiKey, asset)
			if err == nil {
				err = UpsertEtfFlows(ctx, store, rows)
			}
			if err != nil {
				log.Printf("etf flows: %v", err)
				continue
			}
			ok++
		}
		log.Printf("etf flows: synced %d/%d assets", ok, len(assets))
		select {
		case <-stopCh:
			return
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}
