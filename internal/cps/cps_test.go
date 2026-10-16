package cps

import "testing"

func TestParse(t *testing.T) {
	tests := []struct {
		in   string
		size int
		ok   bool
	}{
		{"", 0, true},
		{"<b 0xdeadbeef>", 4, true},
		{"<r 32><b 0xAA><rc 8> <rd 4><t>", 49, true},
		{"<b 0xabc>", 0, false},
		{"<b deadbeef>", 0, false},
		{"<b 0XAA>", 0, false},
		{"<r -1>", 0, false},
		{"<r 0>", 0, false},
		{"<r>", 0, false},
		{"<t 1>", 0, false},
		{"<c>", 0, false},
		{"<d>", 0, false},
		{"<>", 0, false},
		{"<r 10", 0, false},
		{"junk<r 10>", 0, false},
		{"<b 0xAA BB>", 0, false},
	}
	for _, tt := range tests {
		p, err := Parse(tt.in)
		if (err == nil) != tt.ok {
			t.Errorf("Parse(%q) err = %v, want ok=%v", tt.in, err, tt.ok)
			continue
		}
		if tt.ok && p.Size() != tt.size {
			t.Errorf("Parse(%q) size = %d, want %d", tt.in, p.Size(), tt.size)
		}
	}
}

func TestRoundTrip(t *testing.T) {
	in := "<r 32><b 0xdeadbeef><rc 8><rd 4><t>"
	p, err := Parse(in)
	if err != nil {
		t.Fatal(err)
	}
	if p.String() != in {
		t.Errorf("String() = %q, want %q", p.String(), in)
	}
}
