package linkoerr

import (
	"errors"

	"log/slog"
)

type errWithAttrs struct {
	error
	attrs []slog.Attr
}

func (e *errWithAttrs) Unwrap() error { return e.error }

func (e *errWithAttrs) Attrs() []slog.Attr { return e.attrs }

// WithAttrs returns an error that wraps err and carries structured attributes.
func WithAttrs(err error, args ...any) error {
	return &errWithAttrs{
		error: err,
		attrs: argsToAttr(args),
	}
}

// argsToAttr converts a mixed args list into slog.Attr values.
func argsToAttr(args []any) []slog.Attr {
	attrs := make([]slog.Attr, 0, len(args))
	for i := 0; i < len(args); {
		switch v := args[i].(type) {
		case slog.Attr:
			attrs = append(attrs, v)
			i++
		case string:
			if i+1 >= len(args) {
				attrs = append(attrs, slog.String("!BADKEY", v))
				i++
			} else {
				attrs = append(attrs, slog.Any(v, args[i+1]))
				i += 2
			}
		default:
			attrs = append(attrs, slog.Any("!BADKEY", args[i]))
			i++
		}
	}
	return attrs
}

type attrError interface {
	Attrs() []slog.Attr
}

// Attrs extracts all attributes from an error chain (outermost first).
func Attrs(err error) []slog.Attr {
	var out []slog.Attr
	for err != nil {
		if ae, ok := err.(attrError); ok {
			out = append(out, ae.Attrs()...)
		}
		err = errors.Unwrap(err)
	}
	return out
}
