package api

import "testing"

func TestNormalizeNTPServer(t *testing.T) {
	cases := []struct {
		name    string
		in      string
		want    string
		wantErr bool
	}{
		{"bare hostname", "0.th.pool.ntp.org", "0.th.pool.ntp.org", false},
		{"bare IP", "192.168.1.1", "192.168.1.1", false},
		{"empty disables sync", "", "", false},
		{"whitespace-only treated as empty", "   ", "", false},
		{"strips ntp.conf server prefix", "server 0.th.pool.ntp.org", "0.th.pool.ntp.org", false},
		{"strips ntp.conf pool prefix", "pool 0.th.pool.ntp.org", "0.th.pool.ntp.org", false},
		{"strips leading/trailing whitespace", "  0.th.pool.ntp.org  ", "0.th.pool.ntp.org", false},
		{"prefix strip is case-insensitive", "Server 0.th.pool.ntp.org", "0.th.pool.ntp.org", false},
		{"embedded whitespace after strip rejected", "server 0.th.pool.ntp.org extra", "", true},
		{"embedded whitespace with no prefix rejected", "not a hostname", "", true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := normalizeNTPServer(tc.in)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("normalizeNTPServer(%q) = %q, nil; want error", tc.in, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("normalizeNTPServer(%q) unexpected error: %v", tc.in, err)
			}
			if got != tc.want {
				t.Fatalf("normalizeNTPServer(%q) = %q; want %q", tc.in, got, tc.want)
			}
		})
	}
}

// TestNormalizeBrokerURL guards against the 2026-09-03 incident: a leftover
// leading space in mqtt.broker_url (from an earlier placeholder value that
// a later Settings-page edit appended to instead of replacing) made
// net/url.Parse fail, which paho's AddBroker silently swallows into its
// own internal logger - leaving its server list empty and Connect()
// failing with "no servers defined to connect to", which is fatal at
// gateway startup (cmd/gateway/adapter.go). The gateway crash-looped for
// about 5 hours before this was caught. normalizeBrokerURL must reject
// that value at save time instead of writing it to config.yaml.
func TestNormalizeBrokerURL(t *testing.T) {
	cases := []struct {
		name    string
		in      string
		want    string
		wantErr bool
	}{
		{"valid tcp URL", "tcp://mqtt.nxge.co:1883", "tcp://mqtt.nxge.co:1883", false},
		{"valid ssl URL", "ssl://broker.internal:8883", "ssl://broker.internal:8883", false},
		{"leading space auto-trimmed - the real incident, now fixed not just rejected", " tcp://mqtt.nxge.co:1883", "tcp://mqtt.nxge.co:1883", false},
		{"trailing space stripped", "tcp://mqtt.nxge.co:1883 ", "tcp://mqtt.nxge.co:1883", false},
		{"empty rejected", "", "", true},
		{"whitespace-only rejected", "   ", "", true},
		{"no scheme/host rejected", "not a url", "", true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := normalizeBrokerURL(tc.in)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("normalizeBrokerURL(%q) = %q, nil; want error", tc.in, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("normalizeBrokerURL(%q) unexpected error: %v", tc.in, err)
			}
			if got != tc.want {
				t.Fatalf("normalizeBrokerURL(%q) = %q; want %q", tc.in, got, tc.want)
			}
		})
	}
}
