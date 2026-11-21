package pki

import (
	"fmt"
	"testing"
)

func TestNewKeys(t *testing.T) {
	_, err := NewKey()
	if err != nil {
		t.Fatalf("error creating key %v", err)
	}
	fmt.Println("successfully created key")
}
