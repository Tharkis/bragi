package integrations

import (
	"context"
	"errors"
	"fmt"
	"net/url"
)

// UnreachableError means Bragi could not connect to a service at all, or it
// did not answer in time.
type UnreachableError struct {
	Host string
	Err  error
}

func (e *UnreachableError) Error() string {
	// url.Error repeats the whole address, API key included; keep the cause.
	err := e.Err
	var ue *url.Error
	if errors.As(err, &ue) {
		err = ue.Err
	}
	return fmt.Sprintf("could not reach %s: %v", e.Host, err)
}

func (e *UnreachableError) Unwrap() error { return e.Err }

// ReleaseError means a grab failed because of the release itself (or the
// indexer serving it), not because of the download client: the NZB or
// .torrent could not be fetched, or the client refused that file. The next
// release may well work.
type ReleaseError struct{ Err error }

func (e *ReleaseError) Error() string { return e.Err.Error() }
func (e *ReleaseError) Unwrap() error { return e.Err }

func releaseErr(err error) error {
	if err == nil {
		return nil
	}
	return &ReleaseError{Err: err}
}

// IsReleaseError reports whether a grab failed because of the release, so
// the next release is worth trying. Any other grab error is a problem with
// the download client, which the next release would hit too.
func IsReleaseError(err error) bool {
	var re *ReleaseError
	return errors.As(err, &re)
}

// StatusError is an unexpected HTTP status from a service.
type StatusError struct {
	Host string
	Code int
}

func (e *StatusError) Error() string { return fmt.Sprintf("%s answered HTTP %d", e.Host, e.Code) }

// IsTransient reports whether err may go away on its own: a service that
// could not be reached or timed out. Releases that fail this way are not
// blocklisted.
func IsTransient(err error) bool {
	var ue *UnreachableError
	var se *StatusError
	if errors.As(err, &se) && (se.Code >= 500 || se.Code == 429) {
		return true
	}
	return errors.As(err, &ue) || errors.Is(err, context.DeadlineExceeded)
}

// ShouldBlocklist reports whether a failed grab means the release itself is
// bad and should be skipped from now on.
func ShouldBlocklist(err error) bool {
	return IsReleaseError(err) && !IsTransient(err) && !errors.Is(err, ErrUnauthorized)
}
