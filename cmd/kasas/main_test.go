package main

import "testing"

func TestListensBeyondLoopback(t *testing.T) {
	cases := []struct {
		addr string
		want bool
	}{
		{":8080", true},              // empty host binds all interfaces
		{"0.0.0.0:8080", true},       // all IPv4 interfaces
		{"[::]:8080", true},          // all IPv6 interfaces
		{"192.168.1.10:8080", true},  // LAN IP
		{"10.0.0.5:8080", true},      // private IP, still beyond loopback
		{"100.92.46.107:8080", true}, // Tailscale CGNAT IP is non-loopback
		{"example.com:8080", true},   // hostname could resolve anywhere
		{"not-an-addr", true},        // unparseable → fail safe (treat as exposed)
		{"127.0.0.1:8080", false},    // loopback
		{"127.0.0.5:9000", false},    // anywhere in 127.0.0.0/8 is loopback
		{"[::1]:8080", false},        // IPv6 loopback
		{"localhost:8080", false},    // resolves to loopback
	}
	for _, c := range cases {
		if got := listensBeyondLoopback(c.addr); got != c.want {
			t.Errorf("listensBeyondLoopback(%q) = %v, want %v", c.addr, got, c.want)
		}
	}
}

func TestCheckUnauthenticatedExposure(t *testing.T) {
	cases := []struct {
		name          string
		tokenRequired bool
		addr          string
		allow         bool
		wantErr       bool
	}{
		{"token set, exposed: ok", true, ":8080", false, false},
		{"token set, exposed, regardless of opt-in: ok", true, "0.0.0.0:8080", false, false},
		{"no token, loopback: ok", false, "127.0.0.1:8080", false, false},
		{"no token, exposed, opted in: ok", false, ":8080", true, false},
		{"no token, exposed, not opted in: refuse", false, ":8080", false, true},
		{"no token, LAN ip, not opted in: refuse", false, "192.168.1.5:8080", false, true},
	}
	for _, c := range cases {
		err := checkUnauthenticatedExposure(c.tokenRequired, c.addr, c.allow)
		if (err != nil) != c.wantErr {
			t.Errorf("%s: checkUnauthenticatedExposure(%v, %q, %v) err=%v, wantErr=%v",
				c.name, c.tokenRequired, c.addr, c.allow, err, c.wantErr)
		}
	}
}
