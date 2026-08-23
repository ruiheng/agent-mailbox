package waypost

import (
	"fmt"
	"strings"
)

func normalizeDeliveryStateFilter(state string) (string, error) {
	state = strings.TrimSpace(state)
	if state == "" {
		return "", nil
	}

	switch state {
	case "queued", "leased", "acked", "dead_letter":
		return state, nil
	case "claimed":
		return "leased", nil
	default:
		return "", invalidArgumentError(fmt.Errorf("delivery state %q must be one of queued, leased, claimed, acked, dead_letter", state))
	}
}
