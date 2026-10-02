package output

// painter wraps text in ANSI SGR sequences when enabled. Formatters compute
// padding on the plain text and only then call the painter, so colored and
// uncolored output differ solely by escape sequences.
type painter struct{ on bool }

func newPainter(on bool) painter { return painter{on: on} }

const sgrReset = "\x1b[0m"

// wrap applies one SGR code. Empty strings are never wrapped so empty cells
// stay free of stray escapes.
func (p painter) wrap(code, s string) string {
	if !p.on || s == "" {
		return s
	}
	return "\x1b[" + code + "m" + s + sgrReset
}

func (p painter) bold(s string) string   { return p.wrap("1", s) }
func (p painter) dim(s string) string    { return p.wrap("2", s) }
func (p painter) red(s string) string    { return p.wrap("31", s) }
func (p painter) green(s string) string  { return p.wrap("32", s) }
func (p painter) yellow(s string) string { return p.wrap("33", s) }

// Emphasize renders s in bold green when on, for the one line of a run that
// must stand out (the reclaimed size at the end of a sweep).
func Emphasize(on bool, s string) string { return newPainter(on).wrap("1;32", s) }
