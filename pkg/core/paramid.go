package core

import (
	"fmt"
	"strconv"
	"strings"
)

// ParamID names a parameter on a track as "fx<n>.p<m>", both 1-based.
type ParamID string

func MakeParamID(fx, p int) ParamID { return ParamID(fmt.Sprintf("fx%d.p%d", fx, p)) }

// Parse splits the id into its FX and parameter numbers.
func (id ParamID) Parse() (fx, p int, err error) {
	a, b, ok := strings.Cut(string(id), ".")
	if !ok || !strings.HasPrefix(a, "fx") || !strings.HasPrefix(b, "p") {
		return 0, 0, fmt.Errorf("%w: %q", ErrUnknownParam, id)
	}
	fx, err1 := strconv.Atoi(a[2:])
	p, err2 := strconv.Atoi(b[1:])
	if err1 != nil || err2 != nil || fx < 1 || p < 1 {
		return 0, 0, fmt.Errorf("%w: %q", ErrUnknownParam, id)
	}
	return fx, p, nil
}
