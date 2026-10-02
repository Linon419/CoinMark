package service

import (
	"testing"
	"time"
)

func TestNextPullbackScanAlignsToQuarterHourPlus30s(t *testing.T) {
	at := func(s string) time.Time {
		v, err := time.Parse("15:04:05", s)
		if err != nil {
			t.Fatal(err)
		}
		return v
	}
	cases := map[string]string{
		"12:00:10": "12:00:30", // 整刻钟后还没到 30 秒：这一刻钟就扫
		"12:00:30": "12:15:30", // 刚扫完：下一刻钟
		"12:07:00": "12:15:30",
		"12:59:59": "13:00:30",
	}
	for now, want := range cases {
		if got := nextPullbackScan(at(now)); !got.Equal(at(want)) {
			t.Fatalf("now=%s next=%s want=%s", now, got.Format("15:04:05"), want)
		}
	}
}
