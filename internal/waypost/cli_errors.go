package waypost

import (
	"encoding/json"
	"errors"
	"io"
	"strings"
)

type cliErrorDocument struct {
	Status    string `json:"status"`
	ErrorCode string `json:"error_code"`
	Message   string `json:"message"`
	Retryable bool   `json:"retryable"`
	Details   any    `json:"details,omitempty"`
}

type cliReceiveRecoveryClaim struct {
	DeliveryID       string `json:"delivery_id"`
	LeaseToken       string `json:"lease_token"`
	RecipientAddress string `json:"recipient_address"`
	LeaseExpiresAt   string `json:"lease_expires_at"`
}

type cliReceiveRecoveryDetails struct {
	RemainingByStateStatus string                    `json:"remaining_by_state_status"`
	Claims                 []cliReceiveRecoveryClaim `json:"claims"`
}

// CLIRequestsJSON reports whether a canonical JSON command path was selected.
// It accepts the global state-dir option so the process entrypoint can format
// errors after argument preparation has failed.
func CLIRequestsJSON(args []string) bool {
	for _, arg := range args {
		if arg == "--json" || arg == "--json=true" {
			return true
		}
	}
	return false
}

func cliOwnedCommand(args []string) bool {
	for index := 0; index < len(args); index++ {
		arg := args[index]
		switch {
		case arg == "--state-dir":
			index++
			continue
		case strings.HasPrefix(arg, "--state-dir="):
			continue
		case strings.HasPrefix(arg, "-"):
			continue
		}
		switch arg {
		case "forward", "wait", "list", "read", "undefer", "fail", "group", "address":
			return true
		default:
			return false
		}
	}
	return false
}

// WriteCLIJSONError writes the structured failure contract for CLI-owned
// operations and the exceptional receive-recovery result. It leaves normal
// human-oriented command failures untouched.
func WriteCLIJSONError(w io.Writer, args []string, err error) bool {
	if !CLIRequestsJSON(args) || (!cliOwnedCommand(args) && !errors.Is(err, ErrReceiveRecoveryRequired)) {
		return false
	}
	document := cliErrorFor(err)
	_ = json.NewEncoder(w).Encode(document)
	return true
}

func cliErrorFor(err error) cliErrorDocument {
	document := cliErrorDocument{
		Status:    "error",
		ErrorCode: "internal",
		Message:   err.Error(),
		Retryable: false,
	}

	var recovery *ReceiveRecoveryRequiredError
	if errors.As(err, &recovery) {
		claims := make([]cliReceiveRecoveryClaim, 0, len(recovery.Claims))
		for _, claim := range recovery.Claims {
			claims = append(claims, cliReceiveRecoveryClaim{
				DeliveryID:       claim.DeliveryID,
				LeaseToken:       claim.LeaseToken,
				RecipientAddress: claim.RecipientAddress,
				LeaseExpiresAt:   claim.LeaseExpiresAt,
			})
		}
		document.ErrorCode = "receive_recovery_required"
		document.Details = cliReceiveRecoveryDetails{
			RemainingByStateStatus: "unavailable",
			Claims:                 claims,
		}
		return document
	}

	switch {
	case errors.Is(err, ErrBodyIntegrity):
		document.ErrorCode = "integrity_error"
	case isSQLiteBusy(err):
		document.ErrorCode = "busy"
		document.Retryable = true
	case errors.Is(err, ErrLeaseNotFound), errors.Is(err, ErrDeliveryNotFound), errors.Is(err, ErrMessageNotFound), errors.Is(err, ErrGroupNotFound):
		document.ErrorCode = "not_found"
	case errors.Is(err, ErrGroupExists), errors.Is(err, ErrActiveMembershipExists), errors.Is(err, ErrActiveSubscriberExists):
		document.ErrorCode = "already_exists"
	case errors.Is(err, ErrLeaseExpired), errors.Is(err, ErrLeaseNotLeased), errors.Is(err, ErrLeaseRenewChanged), errors.Is(err, ErrLeaseTokenMismatch), errors.Is(err, ErrActiveMembershipMissing), errors.Is(err, ErrActiveSubscriberMissing), errors.Is(err, ErrAddressReservedByEndpoint), errors.Is(err, ErrAddressReservedByGroup):
		document.ErrorCode = "invalid_state"
	case cliInvalidStateError(err):
		document.ErrorCode = "invalid_state"
	case cliInvalidArgumentError(err):
		document.ErrorCode = "invalid_argument"
	}
	return document
}

func cliInvalidStateError(err error) bool {
	message := strings.ToLower(err.Error())
	return strings.Contains(message, "is in state") ||
		strings.Contains(message, "already visible") ||
		strings.Contains(message, "changed while")
}

func cliInvalidArgumentError(err error) bool {
	message := strings.ToLower(err.Error())
	return strings.Contains(message, " is required") ||
		strings.Contains(message, "must ") ||
		strings.Contains(message, "exactly one") ||
		strings.Contains(message, "mutually exclusive") ||
		strings.Contains(message, "not supported") ||
		strings.Contains(message, "provided but not defined") ||
		strings.Contains(message, "needs an argument") ||
		strings.Contains(message, "unknown ") ||
		strings.Contains(message, "parse ") ||
		strings.Contains(message, "invalid ") ||
		strings.Contains(message, "empty")
}
