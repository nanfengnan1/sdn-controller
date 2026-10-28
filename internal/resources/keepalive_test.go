package resources

import (
	a "sdn-agent/api/agent/v1"
	"testing"
)

func TestKeepaliveSingletonSwitchPresence(t *testing.T) {
	on := apply(t, nil, `{"code":"NEIGHBOR_KEEPALIVE","action":"SET","neighborKeepalive":{"enabled":true}}`)
	off := apply(t, on, `{"id":"different","code":"NEIGHBOR_KEEPALIVE","action":"SET","neighborKeepalive":{"enabled":false}}`)
	if len(off) != 1 || off[0].Id != "resource/neighbor_keepalive" || off[0].GetNeighborKeepalive().Enabled == nil || off[0].GetNeighborKeepalive().GetEnabled() {
		t.Fatal(off)
	}
	if !on[0].GetNeighborKeepalive().GetEnabled() {
		t.Fatal("mutated old policy")
	}
	for _, body := range []string{
		`{"code":"NEIGHBOR_KEEPALIVE","action":"SET","neighborKeepalive":{}}`,
		`{"code":"NEIGHBOR_KEEPALIVE","action":"DELETE","neighborKeepalive":{"enabled":false}}`,
	} {
		if _, err := Apply(on, []*a.Operation{op(t, body)}); err == nil {
			t.Fatal("invalid switch accepted", body)
		}
	}
}
