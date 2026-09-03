package api

import (
	"io"
	"log/slog"
	"net"
	"strings"
	"testing"

	mochi "github.com/mochi-mqtt/server/v2"
	"github.com/mochi-mqtt/server/v2/hooks/auth"
	"github.com/mochi-mqtt/server/v2/listeners"

	"nxiiot-gateway/internal/config"
)

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

// startTestBroker runs a real, embedded, pure-Go MQTT broker for the
// duration of the test, matching internal/forwarder's own established
// preference for exercising real infrastructure over hand-rolled fakes
// (see mqttadapter_test.go's startTestBroker) — duplicated here rather
// than exported cross-package, since it's ~15 lines and this is the only
// other place that needs it.
func startTestBroker(t *testing.T) string {
	t.Helper()

	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve free port: %v", err)
	}
	addr := l.Addr().String()
	l.Close()

	server := mochi.New(nil)
	if err := server.AddHook(new(auth.AllowHook), nil); err != nil {
		t.Fatalf("add allow-all hook: %v", err)
	}
	tcp := listeners.NewTCP(listeners.Config{ID: "test", Address: addr})
	if err := server.AddListener(tcp); err != nil {
		t.Fatalf("add tcp listener: %v", err)
	}
	go func() { _ = server.Serve() }()
	t.Cleanup(func() { _ = server.Close() })

	return "tcp://" + addr
}

// TestTestMQTTConnect guards the live-test-connect feature added after the
// 2026-09-03/04 incident (see MEMORY.md): normalizeBrokerURL alone only
// catches a value that can't parse as a URL, not one that's syntactically
// fine but wrong/unreachable - which sails through to the next restart
// and crash-loops the whole gateway exactly like that incident. This
// tests the actual network behavior, not just a mocked outcome.
func TestTestMQTTConnect(t *testing.T) {
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	s := &Server{log: log}

	t.Run("http transport never attempts a connection", func(t *testing.T) {
		err := s.testMQTTConnect(config.MQTTConfig{BrokerURL: "tcp://this-host-does-not-exist.invalid:1883"}, "http")
		if err != nil {
			t.Fatalf("testMQTTConnect with http transport = %v; want nil (should short-circuit before dialing anything)", err)
		}
	})

	t.Run("reachable broker succeeds", func(t *testing.T) {
		brokerURL := startTestBroker(t)
		err := s.testMQTTConnect(config.MQTTConfig{
			BrokerURL:        brokerURL,
			ClientID:         "test-client",
			QoS:              1,
			KeepAliveSec:     30,
			ConnectTimeoutMs: 2000,
		}, "mqtt")
		if err != nil {
			t.Fatalf("testMQTTConnect against a real reachable broker = %v; want nil", err)
		}
	})

	t.Run("unreachable broker is rejected, not left to fail at the next restart", func(t *testing.T) {
		l, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatalf("reserve free port: %v", err)
		}
		addr := l.Addr().String()
		l.Close() // freed but nothing is listening - connection refused

		err = s.testMQTTConnect(config.MQTTConfig{
			BrokerURL:        "tcp://" + addr,
			ClientID:         "test-client",
			ConnectTimeoutMs: 500,
		}, "mqtt")
		if err == nil {
			t.Fatal("testMQTTConnect against an unreachable broker = nil; want an error")
		}
	})

	t.Run("malformed-but-parseable broker (the real incident) is rejected", func(t *testing.T) {
		// normalizeBrokerURL trims this before it would ever reach here in
		// the real saveSettings flow, but testMQTTConnect must independently
		// refuse to silently succeed against a broker list paho ends up
		// treating as empty - this is the defense-in-depth check.
		err := s.testMQTTConnect(config.MQTTConfig{
			BrokerURL:        " tcp://127.0.0.1:1", // leading space, port 1 (nothing there either)
			ClientID:         "test-client",
			ConnectTimeoutMs: 500,
		}, "mqtt")
		if err == nil {
			t.Fatal("testMQTTConnect with a leading-space broker_url = nil; want an error")
		}
		if !strings.Contains(err.Error(), "connect") {
			t.Fatalf("testMQTTConnect error = %q; want it to mention the connect failure", err.Error())
		}
	})
}
