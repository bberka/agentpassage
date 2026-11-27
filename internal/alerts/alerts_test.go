package alerts

import (
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/bberka/agentpassage/internal/config"
)

// Delivery failures reach the error hook, without the webhook URL's token.
func TestDeliveryFailuresAreReported(t *testing.T) {
	t.Setenv("PATH", t.TempDir()) // no notify-send / osascript
	n := New(config.AlertsConfig{Enabled: true, Desktop: true, WebhookURL: "http://127.0.0.1:1/hook/s3cret"})
	errs := make(chan error, 2)
	n.SetErrorHook(func(err error) { errs <- err })
	n.Notify("k", Event{Title: "t", Message: "m"})

	want := 1
	if runtime.GOOS == "linux" || runtime.GOOS == "darwin" {
		want = 2
	}
	for range want {
		select {
		case err := <-errs:
			if strings.Contains(err.Error(), "s3cret") {
				t.Fatalf("webhook token leaked: %v", err)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("delivery failure not reported")
		}
	}
}
