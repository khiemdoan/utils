// errkit implements all errors generated by nuclei and includes error definations
// specific to nuclei , error classification (like network,logic) etc
package errkit

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"github.com/projectdiscovery/utils/env"
	"golang.org/x/exp/maps"
)

const (
	// DelimArrow is delim used by projectdiscovery/utils to join errors
	DelimArrow = "<-"
	// DelimArrowSerialized
	DelimArrowSerialized = "\u003c-"
	// DelimSemiColon is standard delim popularly used to join errors
	DelimSemiColon = "; "
	// DelimMultiLine is delim used to join errors in multiline format
	DelimMultiLine = "\n -  "
	// MultiLinePrefix is the prefix used for multiline errors
	MultiLineErrPrefix = "the following errors occurred:"
)

var (
	// MaxErrorDepth is the maximum depth of errors to be unwrapped or maintained
	// all errors beyond this depth will be ignored
	MaxErrorDepth = env.GetEnvOrDefault("MAX_ERROR_DEPTH", 3)
	// ErrorSeperator is the seperator used to join errors
	ErrorSeperator = env.GetEnvOrDefault("ERROR_SEPERATOR", "; ")
)

// ErrorX is a custom error type that can handle all known types of errors
// wrapping and joining strategies including custom ones and it supports error class
// which can be shown to client/users in more meaningful way
type ErrorX struct {
	kind     ErrKind
	attrs    map[string]slog.Attr
	errs     []error
	uniqErrs map[string]struct{}
}

// append is internal method to append given
// error to error slice , it removes duplicates
func (e *ErrorX) append(errs ...error) {
	if e.uniqErrs == nil {
		e.uniqErrs = make(map[string]struct{})
	}
	for _, err := range errs {
		if _, ok := e.uniqErrs[err.Error()]; ok {
			continue
		}
		e.uniqErrs[err.Error()] = struct{}{}
		e.errs = append(e.errs, err)
	}
}

func (e ErrorX) MarshalJSON() ([]byte, error) {
	tmp := []string{}
	for _, err := range e.errs {
		tmp = append(tmp, err.Error())
	}
	m := map[string]interface{}{
		"kind":   e.kind.String(),
		"errors": tmp,
	}
	if len(e.attrs) > 0 {
		m["attrs"] = slog.GroupValue(maps.Values(e.attrs)...)
	}
	return json.Marshal(m)
}

// Errors returns all errors parsed by the error
func (e *ErrorX) Errors() []error {
	return e.errs
}

// Attrs returns all attributes associated with the error
func (e *ErrorX) Attrs() []slog.Attr {
	if e.attrs == nil {
		return nil
	}
	return maps.Values(e.attrs)
}

// Build returns the object as error interface
func (e *ErrorX) Build() error {
	return e
}

// Unwrap returns the underlying error
func (e *ErrorX) Unwrap() []error {
	return e.errs
}

// Is checks if current error contains given error
func (e *ErrorX) Is(err error) bool {
	x := &ErrorX{}
	parseError(x, err)
	// even one submatch is enough
	for _, orig := range e.errs {
		for _, match := range x.errs {
			if errors.Is(orig, match) {
				return true
			}
		}
	}
	return false
}

// Error returns the error string
func (e *ErrorX) Error() string {
	var sb strings.Builder
	if e.kind != nil && e.kind.String() != "" {
		sb.WriteString("errKind=")
		sb.WriteString(e.kind.String())
		sb.WriteString(" ")
	}
	if len(e.attrs) > 0 {
		sb.WriteString(slog.GroupValue(maps.Values(e.attrs)...).String())
		sb.WriteString(" ")
	}
	for _, err := range e.errs {
		sb.WriteString(err.Error())
		sb.WriteString(ErrorSeperator)
	}
	return strings.TrimSuffix(sb.String(), ErrorSeperator)
}

// Cause return the original error that caused this without any wrapping
func (e *ErrorX) Cause() error {
	if len(e.errs) > 0 {
		return e.errs[0]
	}
	return nil
}

// Kind returns the errorkind associated with this error
// if any
func (e *ErrorX) Kind() ErrKind {
	if e.kind == nil || e.kind.String() == "" {
		return ErrKindUnknown
	}
	return e.kind
}

// FromError parses a given error to understand the error class
// and optionally adds given message for more info
func FromError(err error) *ErrorX {
	if err == nil {
		return nil
	}
	nucleiErr := &ErrorX{}
	parseError(nucleiErr, err)
	return nucleiErr
}

// New creates a new error with the given message
func New(format string, args ...interface{}) *ErrorX {
	e := &ErrorX{}
	e.append(fmt.Errorf(format, args...))
	return e
}

// Msgf adds a message to the error
func (e *ErrorX) Msgf(format string, args ...interface{}) {
	if e == nil {
		return
	}
	e.append(fmt.Errorf(format, args...))
}

// SetClass sets the class of the error
// if underlying error class was already set, then it is given preference
// when generating final error msg
func (e *ErrorX) SetKind(kind ErrKind) *ErrorX {
	if e.kind == nil {
		e.kind = kind
	} else {
		e.kind = CombineErrKinds(e.kind, kind)
	}
	return e
}

// SetAttr sets additional attributes to a given error
// it only adds unique attributes and ignores duplicates
// Note: only key is checked for uniqueness
func (e *ErrorX) SetAttr(s ...slog.Attr) *ErrorX {
	for _, attr := range s {
		if e.attrs == nil {
			e.attrs = make(map[string]slog.Attr)
		}
		// check if this exists
		if _, ok := e.attrs[attr.Key]; !ok && len(e.attrs) < MaxErrorDepth {
			e.attrs[attr.Key] = attr
		}
	}
	return e
}

// parseError recursively parses all known types of errors
func parseError(to *ErrorX, err error) {
	if err == nil {
		return
	}
	if to == nil {
		to = &ErrorX{}
	}
	if len(to.errs) >= MaxErrorDepth {
		return
	}

	switch v := err.(type) {
	case *ErrorX:
		to.append(v.errs...)
		to.kind = CombineErrKinds(to.kind, v.kind)
	case JoinedError:
		foundAny := false
		for _, e := range v.Unwrap() {
			to.append(e)
			foundAny = true
		}
		if !foundAny {
			parseError(to, errors.New(err.Error()))
		}
	case WrappedError:
		if v.Unwrap() != nil {
			parseError(to, v.Unwrap())
		} else {
			parseError(to, errors.New(err.Error()))
		}
	case CauseError:
		to.append(v.Cause())
		remaining := strings.Replace(err.Error(), v.Cause().Error(), "", -1)
		parseError(to, errors.New(remaining))
	default:
		errString := err.Error()
		// try assigning to enriched error
		if strings.Contains(errString, DelimArrow) {
			// Split the error by arrow delim
			parts := strings.Split(errString, DelimArrow)
			for i := len(parts) - 1; i >= 0; i-- {
				part := strings.TrimSpace(parts[i])
				parseError(to, errors.New(part))
			}
		} else if strings.Contains(errString, DelimArrowSerialized) {
			// Split the error by arrow delim
			parts := strings.Split(errString, DelimArrowSerialized)
			for i := len(parts) - 1; i >= 0; i-- {
				part := strings.TrimSpace(parts[i])
				parseError(to, errors.New(part))
			}
		} else if strings.Contains(errString, DelimSemiColon) {
			// Split the error by semi-colon delim
			parts := strings.Split(errString, DelimSemiColon)
			for _, part := range parts {
				part = strings.TrimSpace(part)
				parseError(to, errors.New(part))
			}
		} else if strings.Contains(errString, MultiLineErrPrefix) {
			// remove prefix
			msg := strings.ReplaceAll(errString, MultiLineErrPrefix, "")
			parts := strings.Split(msg, DelimMultiLine)
			for _, part := range parts {
				part = strings.TrimSpace(part)
				parseError(to, errors.New(part))
			}
		} else {
			// this cannot be furthur unwrapped
			to.append(err)
		}
	}
}

// WrappedError is implemented by errors that are wrapped
type WrappedError interface {
	// Unwrap returns the underlying error
	Unwrap() error
}
