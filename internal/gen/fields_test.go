package gen

import "testing"

func TestFieldsRoundTrip(t *testing.T) {
	o, err := Obfuscation(Presets[0])
	if err != nil {
		t.Fatal(err)
	}
	got, err := FieldsOf(o).Apply(o)
	if err != nil {
		t.Fatal(err)
	}
	if got != o {
		t.Fatalf("round trip changed values: %+v != %+v", got, o)
	}
	f := FieldsOf(o)
	f.H2 = "5-1"
	if _, err := f.Apply(o); err == nil {
		t.Fatal("bad range accepted")
	}
	f = FieldsOf(o)
	f.Jc = "x"
	if _, err := f.Apply(o); err == nil {
		t.Fatal("bad number accepted")
	}
}
