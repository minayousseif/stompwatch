package web

import (
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
)

// params reads query parameters and keeps the first fault it finds. A typo
// is never ignored: an owner who reads a filtered list believing it is
// unfiltered has been told something untrue.
type params struct {
	v   url.Values
	err error
}

// parseQuery reads the query string and rejects any name that is not listed.
func parseQuery(r *http.Request, allowed ...string) *params {
	p := &params{}
	v, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil {
		p.err = fmt.Errorf("the address has a query that cannot be read. Check the parameters.")
		return p
	}
	p.v = v
	for name := range v {
		if !slices.Contains(allowed, name) {
			p.fault("%q is not a parameter of this address. It accepts %s.",
				name, strings.Join(allowed, ", "))
			break
		}
	}
	return p
}

func (p *params) fault(format string, args ...any) {
	if p.err == nil {
		p.err = fmt.Errorf(format, args...)
	}
}

// has reports whether the caller gave a non-empty value for name.
func (p *params) has(name string) bool { return p.v.Get(name) != "" }

// ms reads an epoch-millisecond time. It returns 0 when the parameter is
// absent.
func (p *params) ms(name string) int64 {
	s := p.v.Get(name)
	if s == "" {
		return 0
	}
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		p.fault("%s must be a time in epoch milliseconds, not %q.", name, s)
		return 0
	}
	return n
}

// requireMS reads a time that the address cannot do without.
func (p *params) requireMS(name string) int64 {
	if !p.has(name) {
		p.fault("%s is missing. Give the start and end of the range in epoch milliseconds.", name)
		return 0
	}
	return p.ms(name)
}

// intIn reads a whole number and insists on a range. An out-of-range value is
// refused rather than quietly trimmed.
func (p *params) intIn(name string, def, lo, hi int) int {
	s := p.v.Get(name)
	if s == "" {
		return def
	}
	n, err := strconv.Atoi(s)
	if err != nil {
		p.fault("%s must be a whole number, not %q.", name, s)
		return def
	}
	if n < lo || n > hi {
		p.fault("%s must be between %d and %d, not %d.", name, lo, hi, n)
		return def
	}
	return n
}

// int64AtLeast reads a whole number with a floor and no ceiling.
func (p *params) int64AtLeast(name string, def, lo int64) int64 {
	s := p.v.Get(name)
	if s == "" {
		return def
	}
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		p.fault("%s must be a whole number, not %q.", name, s)
		return def
	}
	if n < lo {
		p.fault("%s must be at least %d, not %d.", name, lo, n)
		return def
	}
	return n
}

// float reads a decibel value. It returns nil when the parameter is absent.
func (p *params) float(name string) *float64 {
	s := p.v.Get(name)
	if s == "" {
		return nil
	}
	f, err := strconv.ParseFloat(s, 64)
	if err != nil {
		p.fault("%s must be a number, not %q.", name, s)
		return nil
	}
	return &f
}

// text reads a free-text parameter.
func (p *params) text(name string) string { return p.v.Get(name) }

// one reads a parameter that must be one of a short list.
func (p *params) one(name, def string, allowed ...string) string {
	s := p.v.Get(name)
	if s == "" {
		return def
	}
	if !slices.Contains(allowed, s) {
		p.fault("%s must be %s, not %q.", name, strings.Join(allowed, ", "), s)
		return def
	}
	return s
}

// many reads a repeatable parameter. Every value must be in the list.
func (p *params) many(name string, allowed ...string) []string {
	var out []string
	for _, s := range p.v[name] {
		if s == "" {
			continue
		}
		if !slices.Contains(allowed, s) {
			p.fault("%s must be %s, not %q.", name, strings.Join(allowed, ", "), s)
			return nil
		}
		out = append(out, s)
	}
	return out
}

// flag reads a parameter that is 1 or 0.
func (p *params) flag(name string) bool {
	switch s := p.v.Get(name); s {
	case "":
		return false
	case "1":
		return true
	case "0":
		return false
	default:
		p.fault("%s must be 1 or 0, not %q.", name, s)
		return false
	}
}

// ok reports the first fault to the caller and returns false when there was
// one.
func (p *params) ok(w http.ResponseWriter) bool {
	if p.err == nil {
		return true
	}
	fail(w, http.StatusBadRequest, p.err.Error())
	return false
}

// pathID reads the {id} of a route.
func pathID(r *http.Request) (int64, bool) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		return 0, false
	}
	return id, true
}

// manyIDs reads a repeatable event number. Every value must be a whole
// number above zero, and there may be at most max of them: a number that is
// not an event must never reach a query, and an unbounded list of them must
// never reach a filesystem path.
func (p *params) manyIDs(name string, max int) []int64 {
	values := p.v[name]
	if len(values) > max {
		p.fault("%s may be given at most %d times, not %d.", name, max, len(values))
		return nil
	}
	var out []int64
	for _, s := range values {
		if s == "" {
			continue
		}
		n, err := strconv.ParseInt(s, 10, 64)
		if err != nil || n <= 0 {
			p.fault("%s must be a whole number above zero, not %q.", name, s)
			return nil
		}
		if !slices.Contains(out, n) {
			out = append(out, n)
		}
	}
	return out
}
