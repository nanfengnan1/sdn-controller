package resources

import (
	"fmt"
	"testing"
)

func TestIPv6VTEPResource(t *testing.T) {
	got := apply(t, nil, `{"code":"VXLAN","action":"CREATE","vxlan":{"name":"vx6","vni":10006,"local":"2001:db8::1"}}`)
	if len(got) != 1 || got[0].GetVxlan().Local != "2001:db8::1" {
		t.Fatal(got)
	}
	if err := Validate(got); err != nil {
		t.Fatal(err)
	}
}

func TestInvalidVTEPResource(t *testing.T) {
	for _, local := range []string{"::", "ff02::1", "fe80::1", "fe80::1%eth0", "0.0.0.0", "224.0.0.1"} {
		t.Run(local, func(t *testing.T) {
			got := apply(t, nil, fmt.Sprintf(`{"code":"VXLAN","action":"CREATE","vxlan":{"name":"vx6","vni":10006,"local":%q}}`, local))
			if err := Validate(got); err == nil {
				t.Fatal("accepted unusable VTEP", local)
			}
		})
	}
}
