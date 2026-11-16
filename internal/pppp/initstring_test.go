package pppp

import (
	"reflect"
	"testing"

	"micam/internal/testvec"
)

func TestDecodeInitString(t *testing.T) {
	v := testvec.MustLoad(t)
	for _, c := range v.Init {
		t.Run(c.ID, func(t *testing.T) {
			raw, err := DecodeInitString(c.Enc + c.Suffix)
			if err != nil {
				t.Fatalf("DecodeInitString: %v", err)
			}
			if raw != c.Raw {
				t.Fatalf("raw = %q, want %q", raw, c.Raw)
			}
			if raw[len(raw)-1] != ',' {
				t.Fatalf("the trailing comma must be preserved, got %q", raw)
			}

			servers, err := Servers(c.Enc + c.Suffix)
			if err != nil {
				t.Fatalf("Servers: %v", err)
			}
			got := make([]string, 0, len(servers))
			for _, ip := range servers {
				got = append(got, ip.String())
			}
			if !reflect.DeepEqual(got, c.Servers) {
				t.Fatalf("servers = %v, want %v", got, c.Servers)
			}
		})
	}
}

func TestDecodeInitStringRejectsBadInput(t *testing.T) {
	for _, s := range []string{"", "ABC", "ABZ", "abc"} {
		if _, err := DecodeInitString(s); err == nil {
			t.Fatalf("DecodeInitString(%q) should have failed", s)
		}
	}
}

func TestServersIgnoresSecretSuffix(t *testing.T) {
	v := testvec.MustLoad(t)
	if len(v.Init) == 0 {
		t.Fatal("no initstring vectors")
	}
	c := v.Init[0]
	withSuffix, err := Servers(c.Enc + ":SOMEKEY")
	if err != nil {
		t.Fatalf("Servers: %v", err)
	}
	plain, err := Servers(c.Enc)
	if err != nil {
		t.Fatalf("Servers: %v", err)
	}
	if !reflect.DeepEqual(withSuffix, plain) {
		t.Fatalf("suffix changed the server list: %v vs %v", withSuffix, plain)
	}
}
