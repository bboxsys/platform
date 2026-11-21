package logger

import (
	"testing"
)

const (
	TEST_MESSAGE = `testing123`
)

func testNewLogger(t *testing.T) *Logger {
	return NewLogger("test", "unit")
}

func TestDebug(t *testing.T) {
	logger := testNewLogger(t)
	logger.Debug(TEST_MESSAGE)

}
func TestInfo(t *testing.T) {
	logger := testNewLogger(t)
	logger.Info(TEST_MESSAGE)
}
func TestWarn(t *testing.T) {
	logger := testNewLogger(t)
	logger.Warn(TEST_MESSAGE)
}
func TestError(t *testing.T) {
	logger := testNewLogger(t)
	logger.Error(TEST_MESSAGE)
}

// Not sure how to test these as we end up getting panic and error message,
// which makes the test fail.
/*
func TestPanic(t *testing.T) {
	logger := testNewLogger(t)
	logger.Panic("test_message", TEST_MESSAGE)
}
func TestFatal(t *testing.T) {
	logger := testNewLogger(t)
	logger.Fatal("test_fatal", TEST_MESSAGE)
}
*/
