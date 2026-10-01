package service

import (
	"context"
	"path/filepath"
	"testing"

	"coinmark/api-go/internal/migration"
	"coinmark/api-go/internal/repo/sqlite"
)

func TestParseSoSoSummary(t *testing.T) {
	body := []byte(`{"code":0,"message":"success","data":[
		{"date":"2026-09-30","total_net_inflow":-148687762.2,"total_value_traded":2359178178.5,"total_net_assets":107981652503.05,"cum_net_inflow":57495300315.84},
		{"date":"2026-09-29","total_net_inflow":66194702.4,"total_value_traded":1598218153.2,"total_net_assets":107961129364.43,"cum_net_inflow":57643988078.04}]}`)
	rows, err := parseSoSoSummary(body, "BTC")
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 || rows[0].Asset != "BTC" || rows[0].Date != "2026-09-30" || rows[0].NetInflow != -148687762.2 || rows[1].NetAssets != 107961129364.43 {
		t.Fatalf("解析不对: %+v", rows)
	}
	if _, err := parseSoSoSummary([]byte(`{"code":402901,"message":"Too many requests. Rate limit exceeded."}`), "SUI"); err == nil {
		t.Fatal("非 0 code 应返回错误")
	}
}

func TestSummarizeEtfFlows(t *testing.T) {
	rows := []EtfFlowRow{
		// BTC：按日期倒序给出，汇总应正确排序
		{Asset: "BTC", Date: "2026-09-30", NetInflow: -100, NetAssets: 1000},
		{Asset: "BTC", Date: "2026-09-29", NetInflow: 50},
		{Asset: "BTC", Date: "2026-09-26", NetInflow: 20},
		{Asset: "ETH", Date: "2026-09-30", NetInflow: 30, NetAssets: 500},
		{Asset: "ETH", Date: "2026-09-29", NetInflow: 10},
	}
	got := summarizeEtfFlows(rows, 2)
	if len(got) != 2 || got[0].Asset != "ETH" || got[1].Asset != "BTC" {
		t.Fatalf("应按最新一日净流入从高到低排序: %+v", got)
	}
	btc := got[1]
	if btc.Latest.Date != "2026-09-30" || btc.Latest.NetAssets != 1000 || btc.Sum5 != -30 || btc.Sum20 != -30 {
		t.Fatalf("BTC 汇总不对: %+v", btc)
	}
	if len(btc.Series) != 2 || btc.Series[0].Date != "2026-09-29" || btc.Series[1].Date != "2026-09-30" {
		t.Fatalf("序列应只保留最近 days 天且按时间升序: %+v", btc.Series)
	}
}

func TestEtfFlowsUpsertAndList(t *testing.T) {
	ctx := context.Background()
	store, err := sqlite.Open(filepath.Join(t.TempDir(), "app.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := migration.Migrate(ctx, store); err != nil {
		t.Fatal(err)
	}
	if err := UpsertEtfFlows(ctx, store, []EtfFlowRow{{Asset: "BTC", Date: "2026-09-30", NetInflow: 1}, {Asset: "BTC", Date: "2026-09-29", NetInflow: 2}}); err != nil {
		t.Fatal(err)
	}
	// 同一天再次写入应覆盖（SoSoValue 会修正当天数据）
	if err := UpsertEtfFlows(ctx, store, []EtfFlowRow{{Asset: "BTC", Date: "2026-09-30", NetInflow: 5, NetAssets: 9}}); err != nil {
		t.Fatal(err)
	}
	rows, err := ListEtfFlowRows(ctx, store, "2026-09-01")
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 {
		t.Fatalf("应有 2 行: %+v", rows)
	}
	for _, r := range rows {
		if r.Date == "2026-09-30" && (r.NetInflow != 5 || r.NetAssets != 9) {
			t.Fatalf("应被覆盖为新值: %+v", r)
		}
	}
}

func TestParseSoSoPage(t *testing.T) {
	html := `<html><body><script id="__NEXT_DATA__" type="application/json">{"props":{"pageProps":{"historyData":{"list":[
		{"dataDate":"2026-09-30 00:00:00","totalNetInflow":-5029530.4,"totalVolume":35325463,"totalNetAssets":482952964.95,"cumNetInflow":337446953.24},
		{"dataDate":"2026-09-29 00:00:00","totalNetInflow":1167676.95,"totalVolume":100,"totalNetAssets":1868283.12,"cumNetInflow":2}]}}}}</script></body></html>`
	rows, err := parseSoSoPage(html, "HYPE")
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 || rows[0].Asset != "HYPE" || rows[0].Date != "2026-09-30" || rows[0].NetInflow != -5029530.4 || rows[0].ValueTraded != 35325463 || rows[0].NetAssets != 482952964.95 {
		t.Fatalf("解析不对: %+v", rows)
	}
	if _, err := parseSoSoPage("<html>Attention Required! | Cloudflare</html>", "HYPE"); err == nil {
		t.Fatal("没有页面数据时应返回错误")
	}
}

func TestParseFlareSolverr(t *testing.T) {
	html, err := parseFlareSolverr([]byte(`{"status":"ok","message":"","solution":{"status":200,"response":"<html>ok</html>"}}`))
	if err != nil || html != "<html>ok</html>" {
		t.Fatalf("html=%q err=%v", html, err)
	}
	if _, err := parseFlareSolverr([]byte(`{"status":"error","message":"timeout"}`)); err == nil {
		t.Fatal("status 非 ok 应返回错误")
	}
	if _, err := parseFlareSolverr([]byte(`{"status":"ok","solution":{"status":403,"response":"blocked"}}`)); err == nil {
		t.Fatal("目标页面非 200 应返回错误")
	}
}
