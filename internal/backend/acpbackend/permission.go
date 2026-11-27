package acpbackend

import (
	"regexp"
	"strings"

	"github.com/bberka/agentpassage/internal/backend"
)

var (
	// refusalRe is tried first, so "Disallow" never reads as "allow".
	refusalRe  = regexp.MustCompile(`(?i)reject|deny|denied|disallow|cancel|abort|\b(no|not|never|don'?t)\b`)
	approvalRe = regexp.MustCompile(`(?i)\b(allow|approve|accept|yes|proceed)`)
)

// permissionOption returns the option that answers a permission request, or ""
// when none clearly does. A one-time option wins over a standing one, so apx
// doesn't write "always" rules into the agent's settings when it can avoid it.
func permissionOption(opts []backend.PermissionOption, approve bool) string {
	wanted := "reject"
	if approve {
		wanted = "allow"
	}
	for _, kind := range []string{wanted + "_once", wanted + "_always"} {
		for _, o := range opts {
			if strings.EqualFold(o.Kind, kind) {
				return o.ID
			}
		}
	}
	// Agents that send no standard kind are read from the option's text.
	standing := ""
	for _, o := range opts {
		if k := strings.ToLower(o.Kind); strings.HasPrefix(k, "allow_") || strings.HasPrefix(k, "reject_") {
			continue
		}
		text := strings.NewReplacer("_", " ", "-", " ").Replace(o.Kind + " " + o.ID + " " + o.Name)
		refuses := refusalRe.MatchString(text)
		if refuses == approve || (approve && !approvalRe.MatchString(text)) {
			continue
		}
		if !strings.Contains(strings.ToLower(text), "always") {
			return o.ID
		}
		if standing == "" {
			standing = o.ID
		}
	}
	return standing
}
