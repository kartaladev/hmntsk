package webhook_test

import (
	"context"
	"errors"
	"net/netip"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kartaladev/hmntsk/delivery/webhook"
)

// destination builds a destination for addr on the usual HTTPS port.
func destination(t *testing.T, addr string) webhook.Destination {
	t.Helper()

	ip, err := netip.ParseAddr(addr)
	require.NoError(t, err)

	network := "tcp6"
	if ip.Unmap().Is4() {
		network = "tcp4"
	}

	return webhook.Destination{Network: network, Host: "receiver.example.com", IP: ip, Port: 443}
}

func TestDefaultPolicyAllow(t *testing.T) {
	t.Parallel()

	type testCase struct {
		name   string
		addr   string
		assert func(t *testing.T, err error)
	}

	permitted := func(t *testing.T, err error) {
		t.Helper()
		assert.NoError(t, err)
	}

	refused := func(t *testing.T, err error) {
		t.Helper()

		assert.ErrorIs(t, err, webhook.ErrDestinationRefused)

		var refusal *webhook.DestinationError
		if assert.ErrorAs(t, err, &refusal, "refusal must be a *DestinationError") {
			assert.NotEmpty(t, refusal.Reason, "a refusal must say why")
		}
	}

	cases := []testCase{
		{name: "public ipv4", addr: "93.184.216.34", assert: permitted},
		{name: "public ipv6", addr: "2606:2800:220:1:248:1893:25c8:1946", assert: permitted},

		{name: "ipv4 loopback", addr: "127.0.0.1", assert: refused},
		{name: "ipv4 loopback beyond 127.0.0.1", addr: "127.9.9.9", assert: refused},
		{name: "ipv6 loopback", addr: "::1", assert: refused},
		{name: "ipv4-mapped ipv6 loopback", addr: "::ffff:127.0.0.1", assert: refused},

		{name: "ipv4 link-local", addr: "169.254.1.1", assert: refused},
		{name: "cloud metadata address", addr: "169.254.169.254", assert: refused},
		{name: "ipv4-mapped cloud metadata address", addr: "::ffff:169.254.169.254", assert: refused},
		{name: "ipv6 link-local", addr: "fe80::1", assert: refused},

		{name: "private 10/8", addr: "10.0.0.1", assert: refused},
		{name: "private 172.16/12", addr: "172.16.0.1", assert: refused},
		{name: "private 192.168/16", addr: "192.168.1.1", assert: refused},
		{name: "ipv4-mapped private range", addr: "::ffff:10.0.0.1", assert: refused},
		{name: "ipv6 unique local", addr: "fd00::1", assert: refused},

		{name: "ipv4 unspecified", addr: "0.0.0.0", assert: refused},
		{name: "ipv6 unspecified", addr: "::", assert: refused},
		{name: "ipv4 this-network", addr: "0.1.2.3", assert: refused},
		{name: "ipv4 broadcast", addr: "255.255.255.255", assert: refused},
		{name: "ipv4 shared address space", addr: "100.64.0.1", assert: refused},
		{name: "ipv4 ietf protocol assignments", addr: "192.0.0.1", assert: refused},
		{name: "ipv4 benchmarking range", addr: "198.18.0.1", assert: refused},

		{name: "ipv4 multicast", addr: "224.0.0.1", assert: refused},
		{name: "ipv6 interface-local multicast", addr: "ff01::1", assert: refused},
		{name: "ipv6 link-local multicast", addr: "ff02::1", assert: refused},

		{name: "nat64 embedded loopback", addr: "64:ff9b::7f00:1", assert: refused},
		{name: "nat64 embedded private range", addr: "64:ff9b::a00:1", assert: refused},
		{name: "6to4 embedded private range", addr: "2002:a00:1::1", assert: refused},
		{name: "6to4 embedded public address", addr: "2002:5db8:d822::1", assert: permitted},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			err := webhook.DefaultPolicy{}.Allow(t.Context(), destination(t, tc.addr))
			tc.assert(t, err)
		})
	}
}

func TestAllowLoopback(t *testing.T) {
	t.Parallel()

	type testCase struct {
		name   string
		addr   string
		assert func(t *testing.T, err error)
	}

	cases := []testCase{
		{
			name: "loopback is permitted",
			addr: "127.0.0.1",
			assert: func(t *testing.T, err error) {
				assert.NoError(t, err)
			},
		},
		{
			name: "ipv6 loopback is permitted",
			addr: "::1",
			assert: func(t *testing.T, err error) {
				assert.NoError(t, err)
			},
		},
		{
			name: "everything else keeps the default verdict",
			addr: "169.254.169.254",
			assert: func(t *testing.T, err error) {
				assert.ErrorIs(t, err, webhook.ErrDestinationRefused)
			},
		},
		{
			name: "a public address is still permitted",
			addr: "93.184.216.34",
			assert: func(t *testing.T, err error) {
				assert.NoError(t, err)
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			err := webhook.AllowLoopback().Allow(t.Context(), destination(t, tc.addr))
			tc.assert(t, err)
		})
	}
}

func TestPolicyFuncAdaptsAFunction(t *testing.T) {
	t.Parallel()

	sentinel := errors.New("refused by the host")

	var seen webhook.Destination

	policy := webhook.PolicyFunc(func(_ context.Context, dest webhook.Destination) error {
		seen = dest

		return sentinel
	})

	err := policy.Allow(t.Context(), destination(t, "93.184.216.34"))

	assert.ErrorIs(t, err, sentinel)
	assert.Equal(t, netip.MustParseAddr("93.184.216.34"), seen.IP)
	assert.Equal(t, uint16(443), seen.Port)
	assert.Equal(t, "receiver.example.com", seen.Host)
}
