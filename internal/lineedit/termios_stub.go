//go:build !(darwin || linux)

package lineedit

import "errors"

// termState is a placeholder on platforms without termios support; Open fails
// and callers fall back to plain line-at-a-time reading.
type termState struct{}

var errUnsupported = errors.New("lineedit: unsupported platform")

func getTermios(fd int) (*termState, error)  { return nil, errUnsupported }
func setTermios(fd int, st *termState) error { return errUnsupported }
func makeCbreak(st termState) termState      { return st }
