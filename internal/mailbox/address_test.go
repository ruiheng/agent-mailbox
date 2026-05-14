package mailbox

import "testing"

func TestParseAddressAllowsGenericSpecialCharacters(t *testing.T) {
	t.Parallel()

	parsed, err := ParseAddress("workflow/收件箱+tag@example.com")
	if err != nil {
		t.Fatalf("ParseAddress() error = %v", err)
	}
	if parsed.Scheme != "workflow" {
		t.Fatalf("scheme = %q, want workflow", parsed.Scheme)
	}
	if parsed.ID != "收件箱+tag@example.com" {
		t.Fatalf("id = %q, want generic id preserved", parsed.ID)
	}
}

func TestParseAddressRejectsMissingID(t *testing.T) {
	t.Parallel()

	if _, err := ParseAddress("agent-deck"); err == nil {
		t.Fatal("ParseAddress() error = nil, want invalid address")
	}
}

func TestParseAddressRejectsEmptyIDSegment(t *testing.T) {
	t.Parallel()

	if _, err := ParseAddress("workflow/reviewer//task"); err == nil {
		t.Fatal("ParseAddress() error = nil, want empty id segment rejection")
	}
}

func TestParseAddressRejectsNestedKnownSessionAddress(t *testing.T) {
	t.Parallel()

	if _, err := ParseAddress("agent-deck/reviewer/task"); err == nil {
		t.Fatal("ParseAddress() error = nil, want known-session nested path rejection")
	}
}

func TestParseAddressAcceptsKnownSessionSingleTarget(t *testing.T) {
	t.Parallel()

	parsed, err := ParseAddress("codex/550e8400-e29b-41d4-a716-446655440000")
	if err != nil {
		t.Fatalf("ParseAddress() error = %v", err)
	}
	if len(parsed.Segments) != 1 || parsed.Segments[0] != "550e8400-e29b-41d4-a716-446655440000" {
		t.Fatalf("segments = %v, want single target segment", parsed.Segments)
	}
}

func TestNormalizeGroupAddressRequiresGroupScheme(t *testing.T) {
	t.Parallel()

	if _, err := NormalizeGroupAddress("workflow/reviewer"); err == nil {
		t.Fatal("NormalizeGroupAddress() error = nil, want group prefix rejection")
	}

	normalized, err := NormalizeGroupAddress(" group/reviewer ")
	if err != nil {
		t.Fatalf("NormalizeGroupAddress(group) error = %v", err)
	}
	if normalized != "group/reviewer" {
		t.Fatalf("normalized = %q, want group/reviewer", normalized)
	}
}
