package main_test

import (
	"net/http"
	"testing"

	fjson "github.com/0delta/fjson"
)

func TestSimple(t *testing.T) {
	b, err := fjson.Marshal(http.Request{})
	if err != nil {
		t.Fatalf("marshal err %v:", err)
	}
	t.Logf("result %v", string(b))
}
