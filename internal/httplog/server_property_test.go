package httplog

import (
	"fmt"
	"testing"

	"pgregory.net/rapid"
)

// Feature: parental-control-service, Property 16: Валидация IP-адресов для LAN-доступа
// **Validates: Requirements 14.3, 14.4**

// TestPropertyLANAddressAccepted generates random IPs from LAN ranges and asserts
// that IsLANAddress returns true for all of them.
func TestPropertyLANAddressAccepted(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		// Pick one of the four LAN ranges
		rangeIdx := rapid.IntRange(0, 3).Draw(t, "rangeIdx")

		var ip string
		switch rangeIdx {
		case 0:
			// 192.168.0.0/16
			o3 := rapid.IntRange(0, 255).Draw(t, "o3")
			o4 := rapid.IntRange(0, 255).Draw(t, "o4")
			ip = fmt.Sprintf("192.168.%d.%d", o3, o4)
		case 1:
			// 10.0.0.0/8
			o2 := rapid.IntRange(0, 255).Draw(t, "o2")
			o3 := rapid.IntRange(0, 255).Draw(t, "o3")
			o4 := rapid.IntRange(0, 255).Draw(t, "o4")
			ip = fmt.Sprintf("10.%d.%d.%d", o2, o3, o4)
		case 2:
			// 172.16.0.0/12 → 172.16.0.0 – 172.31.255.255
			o2 := rapid.IntRange(16, 31).Draw(t, "o2")
			o3 := rapid.IntRange(0, 255).Draw(t, "o3")
			o4 := rapid.IntRange(0, 255).Draw(t, "o4")
			ip = fmt.Sprintf("172.%d.%d.%d", o2, o3, o4)
		case 3:
			// 127.0.0.0/8
			o2 := rapid.IntRange(0, 255).Draw(t, "o2")
			o3 := rapid.IntRange(0, 255).Draw(t, "o3")
			o4 := rapid.IntRange(0, 255).Draw(t, "o4")
			ip = fmt.Sprintf("127.%d.%d.%d", o2, o3, o4)
		}

		if !IsLANAddress(ip) {
			t.Fatalf("IsLANAddress(%q) = false, expected true (LAN range %d)", ip, rangeIdx)
		}
	})
}

// TestPropertyNonLANAddressRejected generates random IPs that do NOT belong to any
// LAN range and asserts that IsLANAddress returns false for all of them.
// Covers three categories:
//   - First octet not in {10, 127, 172, 192} (completely outside LAN prefixes)
//   - 172.x.y.z where x < 16 or x > 31 (outside 172.16.0.0/12)
//   - 192.x.y.z where x != 168 (outside 192.168.0.0/16)
func TestPropertyNonLANAddressRejected(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		// Pick one of three non-LAN generation strategies
		strategy := rapid.IntRange(0, 2).Draw(t, "strategy")

		var ip string
		switch strategy {
		case 0:
			// First octet not in {10, 127, 172, 192}
			o1 := rapid.IntRange(1, 254).Filter(func(v int) bool {
				return v != 10 && v != 127 && v != 172 && v != 192
			}).Draw(t, "o1")
			o2 := rapid.IntRange(0, 255).Draw(t, "o2")
			o3 := rapid.IntRange(0, 255).Draw(t, "o3")
			o4 := rapid.IntRange(0, 255).Draw(t, "o4")
			ip = fmt.Sprintf("%d.%d.%d.%d", o1, o2, o3, o4)
		case 1:
			// 172.x.y.z where x < 16 or x > 31 (outside 172.16.0.0/12)
			o2 := rapid.IntRange(0, 255).Filter(func(v int) bool {
				return v < 16 || v > 31
			}).Draw(t, "o2")
			o3 := rapid.IntRange(0, 255).Draw(t, "o3")
			o4 := rapid.IntRange(0, 255).Draw(t, "o4")
			ip = fmt.Sprintf("172.%d.%d.%d", o2, o3, o4)
		case 2:
			// 192.x.y.z where x != 168 (outside 192.168.0.0/16)
			o2 := rapid.IntRange(0, 255).Filter(func(v int) bool {
				return v != 168
			}).Draw(t, "o2")
			o3 := rapid.IntRange(0, 255).Draw(t, "o3")
			o4 := rapid.IntRange(0, 255).Draw(t, "o4")
			ip = fmt.Sprintf("192.%d.%d.%d", o2, o3, o4)
		}

		if IsLANAddress(ip) {
			t.Fatalf("IsLANAddress(%q) = true, expected false (non-LAN IP, strategy %d)", ip, strategy)
		}
	})
}
