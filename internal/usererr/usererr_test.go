package usererr

import (
	"errors"
	"fmt"
	"testing"
)

func TestMessage(t *testing.T) {
	err := fmt.Errorf("saving: %w", New("Enter a port number, such as %d.", 2222))
	if msg, ok := Message(err); !ok || msg != "Enter a port number, such as 2222." {
		t.Errorf("wrapped: %q, %v", msg, ok)
	}
	if msg, ok := Message(errors.New("disk full")); ok || msg != "" {
		t.Errorf("a server error: %q, %v", msg, ok)
	}
	if _, ok := Message(nil); ok {
		t.Error("nil: want false")
	}
}
