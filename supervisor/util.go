package supervisor

import (
	"fmt"

	"github.com/pkg/errors"
)

// AssertTruef is AssertTrue with extra info.
func AssertTruef(b bool, format string, args ...interface{}) {
	if !b {
		l.Fatal(fmt.Sprintf("%+v", errors.Errorf(format, args...)))
	}
}
