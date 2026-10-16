package awgconf

import (
	"encoding/json"
	"testing"
)

func TestKeyJSON(t *testing.T) {
	k, err := NewPrivateKey()
	if err != nil {
		t.Fatal(err)
	}
	b, err := json.Marshal(struct{ K Key }{k})
	if err != nil {
		t.Fatal(err)
	}
	if string(b) != `{"K":"`+k.String()+`"}` {
		t.Fatalf("got %s", b)
	}
	var v struct{ K Key }
	if err := json.Unmarshal(b, &v); err != nil || v.K != k {
		t.Fatalf("round trip: %v %v", err, v.K == k)
	}
	if err := json.Unmarshal([]byte(`{"K":"bad"}`), &v); err == nil {
		t.Fatal("bad key accepted")
	}
}
