package waypost

import (
	"errors"
	"testing"
)

func TestClassifiedErrorsPreserveCauseAndMessage(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name           string
		classification error
		classify       func(error) error
	}{
		{name: "invalid argument", classification: ErrInvalidArgument, classify: invalidArgumentError},
		{name: "marked invalid argument", classification: ErrInvalidArgument, classify: MarkInvalidArgument},
		{name: "invalid state", classification: ErrInvalidState, classify: invalidStateError},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			cause := errors.New("actionable detail")
			err := test.classify(cause)
			if err.Error() != cause.Error() {
				t.Fatalf("classified error text = %q, want %q", err.Error(), cause.Error())
			}
			if !errors.Is(err, test.classification) {
				t.Fatalf("classified error = %v, want classification %v", err, test.classification)
			}
			if !errors.Is(err, cause) {
				t.Fatalf("classified error = %v, want cause %v", err, cause)
			}
			if got := test.classify(err); got != err {
				t.Fatalf("classifying twice returned %p, want original %p", got, err)
			}
		})
	}
}
