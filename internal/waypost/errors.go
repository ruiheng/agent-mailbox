package waypost

import "errors"

var (
	// ErrInvalidArgument marks a caller-supplied value that violates an input
	// contract while preserving the original actionable error text.
	ErrInvalidArgument = errors.New("invalid argument")
	// ErrInvalidState marks a valid operation that cannot apply to the current
	// durable state while preserving the original actionable error text.
	ErrInvalidState = errors.New("invalid state")

	errInternalCLI = errors.New("internal CLI error")
)

type classifiedError struct {
	classification error
	cause          error
}

func (e *classifiedError) Error() string {
	return e.cause.Error()
}

func (e *classifiedError) Unwrap() []error {
	return []error{e.classification, e.cause}
}

func classifyError(err, classification error) error {
	if err == nil || errors.Is(err, classification) {
		return err
	}
	return &classifiedError{classification: classification, cause: err}
}

// MarkInvalidArgument annotates err as a caller input failure while preserving
// the original error text and cause.
func MarkInvalidArgument(err error) error {
	return classifyError(err, ErrInvalidArgument)
}

func invalidArgumentError(err error) error {
	return MarkInvalidArgument(err)
}

func invalidStateError(err error) error {
	return classifyError(err, ErrInvalidState)
}

func internalCLIError(err error) error {
	return classifyError(err, errInternalCLI)
}
