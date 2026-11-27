package quota

import (
	"testing"
	"time"
)

func TestParseGrokBillingWeekly(t *testing.T) {
	raw := []byte(`{"config":{"creditUsagePercent":12.34,"currentPeriod":{"type":"USAGE_PERIOD_TYPE_WEEKLY","start":"2026-11-22T18:06:54.115+00:00","end":"2026-11-29T18:06:54.115+00:00"},"prepaidBalance":{}}}`)
	q, err := ParseGrokBilling(raw)
	if err != nil {
		t.Fatal(err)
	}
	w := q.Primary
	if w == nil || w.Label != "Weekly" || w.UsedPercent != 12.3 || w.LeftPercent != 87.7 {
		t.Fatalf("unexpected window %+v", w)
	}
	if w.WindowMins != 7*24*60 {
		t.Fatalf("want a 7-day window, got %d mins", w.WindowMins)
	}
	want := time.Date(2026, 11, 29, 18, 6, 54, 115e6, time.UTC)
	if w.ResetsAt == nil || !w.ResetsAt.Equal(want) {
		t.Fatalf("want reset %v, got %v", want, w.ResetsAt)
	}
}

// A weekly or monthly period with no usage reported yet counts as 0% used.
func TestParseGrokBillingNoUsageYet(t *testing.T) {
	q, err := ParseGrokBilling([]byte(`{"config":{"currentPeriod":{"type":"USAGE_PERIOD_TYPE_MONTHLY","start":"2026-11-01T00:00:00Z","end":"2026-12-01T00:00:00Z"}}}`))
	if err != nil || q.Primary == nil || q.Primary.Label != "Monthly" || q.Primary.UsedPercent != 0 {
		t.Fatalf("got %+v, %v", q.Primary, err)
	}
	if _, err := ParseGrokBilling([]byte(`{"config":{}}`)); err == nil {
		t.Fatal("expected an error when no usage and no period are reported")
	}
}
