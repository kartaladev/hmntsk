package webhook

import (
	"context"
	"net/netip"
)

// Destination is one resolved address the sink is about to connect to.
//
// It carries the resolved IP rather than the hostname alone, because the
// hostname is what a caller controls and the IP is what a connection actually
// reaches. A policy that judges the text of an address is judging something an
// attacker chooses; a policy that judges this is judging what will happen.
type Destination struct {
	// Network is the network the connection will use: "tcp4" or "tcp6".
	Network string
	// Host is the hostname from the callback address, before resolution. It is
	// context for a policy that keeps an allow list of names, and is never a
	// substitute for IP.
	Host string
	// IP is the address the host resolved to, as the dialler will use it.
	IP netip.Addr
	// Port is the port the connection will use.
	Port uint16
}

// DestinationPolicy decides whether the sink may connect to a resolved address.
//
// It is consulted at dial time, once per connection, after resolution and
// before the socket is connected — not against the callback address as text. A
// name that resolves to a public address when the URL is inspected and to an
// internal one microseconds later defeats any check made earlier, so there is
// no earlier check to defeat.
//
// An implementation must be safe for concurrent use: a sink dials from as many
// goroutines as the host relays from.
type DestinationPolicy interface {
	// Allow returns nil to permit the connection, and an error explaining the
	// refusal otherwise. The error is recorded as the outbox entry's last
	// error, so it should name the reason and nothing confidential.
	Allow(ctx context.Context, dest Destination) error
}

// PolicyFunc adapts a function to the [DestinationPolicy] interface.
type PolicyFunc func(ctx context.Context, dest Destination) error

// Allow implements [DestinationPolicy].
func (f PolicyFunc) Allow(ctx context.Context, dest Destination) error { return f(ctx, dest) }

// DefaultPolicy refuses every destination that is not a routable public
// address. Its zero value is ready to use, and it is what a sink uses when the
// host supplies no policy of its own.
//
// It refuses, in this order: an address that is not valid, the unspecified
// address, loopback, link-local unicast — which is where the cloud metadata
// service at 169.254.169.254 lives — any multicast address, the IPv4 broadcast
// address, RFC 1918 private ranges and their RFC 4193 IPv6 equivalent, RFC 6598
// shared address space, 0.0.0.0/8, 192.0.0.0/24 and 198.18.0.0/15.
//
// An IPv4 address arriving in IPv6 clothing is judged as the IPv4 address it
// is: an IPv4-mapped address such as ::ffff:127.0.0.1, a NAT64 address in
// 64:ff9b::/96 or 64:ff9b:1::/48, and a 6to4 address in 2002::/16 are all
// unwrapped before the rules above run. Without that, every rule here is one
// notation away from being bypassed.
//
// It does not, and cannot, refuse a public address that a host's own network
// routes somewhere private. A host that runs split-horizon DNS or a transparent
// proxy knows things this policy does not, and should supply its own.
type DefaultPolicy struct{}

// Address ranges that are not routable on the public internet, and so are
// either inside the host's network or inside nobody's.
var (
	// nat64WellKnown is RFC 6052's well-known prefix for embedding IPv4.
	nat64WellKnown = netip.MustParsePrefix("64:ff9b::/96")
	// nat64LocalUse is RFC 8215's local-use prefix for the same purpose.
	nat64LocalUse = netip.MustParsePrefix("64:ff9b:1::/48")
	// sixToFour is RFC 3056's prefix, which carries an IPv4 address in the two
	// to six bytes after it.
	sixToFour = netip.MustParsePrefix("2002::/16")

	// refusedPrefixes are the IPv4 ranges no netip predicate covers.
	refusedPrefixes = []struct {
		prefix netip.Prefix
		reason string
	}{
		{netip.MustParsePrefix("0.0.0.0/8"), "this-network address (RFC 1122)"},
		{netip.MustParsePrefix("100.64.0.0/10"), "shared address space (RFC 6598)"},
		{netip.MustParsePrefix("192.0.0.0/24"), "IETF protocol assignment (RFC 6890)"},
		{netip.MustParsePrefix("198.18.0.0/15"), "benchmarking address (RFC 2544)"},
		{netip.MustParsePrefix("255.255.255.255/32"), "broadcast address"},
	}
)

// Allow implements [DestinationPolicy].
func (DefaultPolicy) Allow(_ context.Context, dest Destination) error {
	reason := refusalReason(dest.IP)
	if reason == "" {
		return nil
	}

	return &DestinationError{Network: dest.Network, IP: dest.IP, Port: dest.Port, Reason: reason}
}

// refusalReason names the rule that refuses ip, and is empty when none does.
func refusalReason(ip netip.Addr) string {
	if !ip.IsValid() {
		return "not a valid IP address"
	}

	ip = unwrapIPv4(ip)

	switch {
	case ip.IsUnspecified():
		return "unspecified address"
	case ip.IsLoopback():
		return "loopback address"
	case ip.IsLinkLocalUnicast():
		return "link-local address, which is where a cloud metadata service lives"
	case ip.IsInterfaceLocalMulticast(), ip.IsLinkLocalMulticast(), ip.IsMulticast():
		return "multicast address"
	case ip.IsPrivate() && ip.Is4():
		return "private-range address (RFC 1918)"
	case ip.IsPrivate():
		return "unique-local address (RFC 4193)"
	}

	if !ip.Is4() {
		return ""
	}

	for _, refused := range refusedPrefixes {
		if refused.prefix.Contains(ip) {
			return refused.reason
		}
	}

	return ""
}

// unwrapIPv4 returns the IPv4 address an IPv6 address stands for, and the
// address unchanged when it stands for none.
//
// Judging 64:ff9b::7f00:1 as "some IPv6 address" would permit a connection to
// 127.0.0.1 through a NAT64 gateway, which is the whole point of the notation.
func unwrapIPv4(ip netip.Addr) netip.Addr {
	if ip.Is4In6() {
		return ip.Unmap()
	}

	if !ip.Is6() {
		return ip
	}

	octets := ip.As16()

	switch {
	case nat64WellKnown.Contains(ip), nat64LocalUse.Contains(ip):
		return netip.AddrFrom4([4]byte(octets[12:16]))
	case sixToFour.Contains(ip):
		return netip.AddrFrom4([4]byte(octets[2:6]))
	default:
		return ip
	}
}

// AllowLoopback returns a policy that permits loopback destinations and refers
// every other address to [DefaultPolicy].
//
// It exists because a receiver on localhost is a legitimate configuration — a
// sidecar, a test — and because the alternative, weakening the default so that
// tests can reach an httptest server, would weaken it for everybody. Nothing
// else is relaxed: a delivery aimed at the metadata service is still refused.
func AllowLoopback() DestinationPolicy {
	return PolicyFunc(func(ctx context.Context, dest Destination) error {
		if unwrapIPv4(dest.IP).IsLoopback() {
			return nil
		}

		return DefaultPolicy{}.Allow(ctx, dest)
	})
}
