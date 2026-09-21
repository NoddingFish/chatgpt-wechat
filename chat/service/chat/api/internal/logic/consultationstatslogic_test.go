package logic

import (
	"testing"
	"time"

	"chat/service/chat/api/internal/types"
)

func TestConsultationDateRange(t *testing.T) {
	location, err := time.LoadLocation("Asia/Shanghai")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, time.September, 9, 12, 0, 0, 0, location)

	tests := []struct {
		name      string
		req       types.ConsultationStatsReq
		wantStart string
		wantEnd   string
	}{
		{name: "week", req: types.ConsultationStatsReq{Period: "week"}, wantStart: "2026-09-07", wantEnd: "2026-09-14"},
		{name: "month", req: types.ConsultationStatsReq{Period: "month"}, wantStart: "2026-09-01", wantEnd: "2026-10-01"},
		{name: "custom", req: types.ConsultationStatsReq{StartDate: "2026-09-06", EndDate: "2026-09-12"}, wantStart: "2026-09-06", wantEnd: "2026-09-13"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			start, end, err := consultationDateRange(&test.req, now, location)
			if err != nil {
				t.Fatal(err)
			}
			if got := start.Format(time.DateOnly); got != test.wantStart {
				t.Fatalf("start = %s, want %s", got, test.wantStart)
			}
			if got := end.Format(time.DateOnly); got != test.wantEnd {
				t.Fatalf("end = %s, want %s", got, test.wantEnd)
			}
		})
	}
}

func TestConsultationDateRangeRejectsIncompleteRange(t *testing.T) {
	location, _ := time.LoadLocation("Asia/Shanghai")
	_, _, err := consultationDateRange(&types.ConsultationStatsReq{StartDate: "2026-09-06"}, time.Now(), location)
	if err == nil {
		t.Fatal("expected an error")
	}
}
