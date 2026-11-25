package privacy

import (
	"strings"
	"testing"

	"github.com/bberka/agentpassage/internal/config"
)

func TestPrivacyFilterRedactAndRestore(t *testing.T) {
	f := NewFilter(config.PrivacyConfig{
		Enabled:       true,
		RedactAPIKeys: true,
		RedactEmails:  true,
		RedactPhones:  true,
		CustomPatterns: []config.PrivacyPattern{
			{Name: "acct", Pattern: `ACCT-\d{6}`},
		},
	})
	sess := f.NewSession()

	prompt := "Use key sk-proj-abcdefghijklmnop123456, contact alice@example.com or +1 (415) 555-2671, ref ACCT-123456 on 2026-11-25."
	redacted := sess.RedactText(prompt)

	for _, secret := range []string{"sk-proj-abcdefghijklmnop123456", "alice@example.com", "+1 (415) 555-2671", "ACCT-123456"} {
		if strings.Contains(redacted, secret) {
			t.Fatalf("secret %q leaked in redacted text: %s", secret, redacted)
		}
	}
	if !strings.Contains(redacted, "2026-11-25") {
		t.Fatalf("date 2026-11-25 should not be redacted: %s", redacted)
	}
	if !strings.Contains(redacted, "{{APX_KEY_1}}") || !strings.Contains(redacted, "{{APX_EMAIL_1}}") || !strings.Contains(redacted, "{{APX_PHONE_1}}") {
		t.Fatalf("expected reversible placeholders, got: %s", redacted)
	}

	// Same value maps deterministically to the same placeholder.
	redactedAgain := sess.RedactText("Repeat alice@example.com")
	if redactedAgain != "Repeat {{APX_EMAIL_1}}" {
		t.Fatalf("expected stable placeholder reuse, got: %s", redactedAgain)
	}

	// Non-streaming restore brings back exact originals.
	upstreamReply := "Sent to {{APX_EMAIL_1}} using {{APX_KEY_1}} and called {{APX_PHONE_1}} for {{APX_ACCT_1}}."
	restored := sess.RestoreText(upstreamReply)
	want := "Sent to alice@example.com using sk-proj-abcdefghijklmnop123456 and called +1 (415) 555-2671 for ACCT-123456."
	if restored != want {
		t.Fatalf("restore mismatch:\ngot:  %s\nwant: %s", restored, want)
	}

	// Header summary reports categories without leaking raw secrets.
	hdr := sess.HeaderValue()
	if !strings.Contains(hdr, "api_key=1") || !strings.Contains(hdr, "email=1") || !strings.Contains(hdr, "phone=1") {
		t.Fatalf("unexpected header value: %q", hdr)
	}
	sum := sess.Summary()
	if sum.Total != 4 {
		t.Fatalf("expected 4 redacted items, got %+v", sum)
	}
}

func TestStreamRestorerAcrossSplitChunks(t *testing.T) {
	f := NewFilter(config.PrivacyConfig{
		Enabled:      true,
		RedactEmails: true,
		RedactPhones: true,
	})
	sess := f.NewSession()
	_ = sess.RedactText("Email bob@corp.io or call 415-555-0199")

	sr := sess.NewStreamRestorer()
	chunks := []string{"Reach ", "{", "{APX_", "EMAIL_", "1}}", " or call {", "{APX_PHONE", "_1}} now!"}
	var out strings.Builder
	for _, c := range chunks {
		out.WriteString(sr.Push(c))
	}
	out.WriteString(sr.Flush())

	want := "Reach bob@corp.io or call 415-555-0199 now!"
	if out.String() != want {
		t.Fatalf("stream restore mismatch:\ngot:  %q\nwant: %q", out.String(), want)
	}
}

func BenchmarkPrivacyRedactAndStreamRestore(b *testing.B) {
	f := NewFilter(config.PrivacyConfig{
		Enabled:       true,
		RedactAPIKeys: true,
		RedactEmails:  true,
		RedactPhones:  true,
	})
	prompt := "Use key sk-proj-abcdefghijklmnop123456, contact alice@example.com or +1 (415) 555-2671 on 2026-11-25."
	chunks := []string{"Hello ", "world ", "from ", "{{APX_", "EMAIL_", "1}} ", "and normal text."}
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		sess := f.NewSession()
		_ = sess.RedactText(prompt)
		sr := sess.NewStreamRestorer()
		for _, c := range chunks {
			_ = sr.Push(c)
		}
		_ = sr.Flush()
	}
}
