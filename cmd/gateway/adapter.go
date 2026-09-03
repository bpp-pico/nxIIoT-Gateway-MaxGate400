package main

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"nxiiot-gateway/internal/config"
	"nxiiot-gateway/internal/forwarder"
)

// buildAdapter selects the forwarder.Adapter named by cfg.Forwarder.Transport
// (§15: the transport must be swappable behind forwarder.Adapter without
// touching the state machine) and returns a cleanup func to release it on
// shutdown. For "mqtt" this also attempts the initial Connect, but a
// failure there is NOT fatal (changed 2026-09-04 — see MEMORY.md): it used
// to os.Exit(1), which took down Modbus acquisition and the API/web UI
// along with forwarding, for as long as the broker stayed wrong or
// unreachable — a real ~5 hour production outage happened this way twice
// in the same session, once from a malformed broker_url and once from
// the class of bug this change targets (a syntactically valid but
// unreachable broker). Rule 1 ("acquisition never depends on the server")
// already covers a broker going down *after* a successful connect; this
// closes the one remaining gap where it didn't apply — the very first
// connect attempt. A failed initial connect now just logs a warning and
// falls through to the same success path: RunReconnectWatchdog (started
// below either way) picks it up from there, exactly as it already does
// for a connection lost after a successful start. Only genuinely
// unrecoverable-by-waiting errors (bad TLS config, an unrecognized
// transport string) remain fatal below.
func buildAdapter(ctx context.Context, cfg *config.Config, log *slog.Logger) (forwarder.Adapter, func(), error) {
	switch cfg.Forwarder.Transport {
	case "", "http":
		adapter := forwarder.NewHTTPAdapter(cfg.Forwarder.ServerURL, time.Duration(cfg.Forwarder.SendTimeoutMs)*time.Millisecond)
		return adapter, func() {}, nil

	case "mqtt":
		tlsConfig, err := config.BuildMQTTTLSConfig(cfg.MQTT.TLS)
		if err != nil {
			return nil, nil, fmt.Errorf("mqtt tls config: %w", err)
		}

		adapter := forwarder.NewMQTTAdapter(forwarder.MQTTAdapterConfig{
			BrokerURL:      cfg.MQTT.BrokerURL,
			ClientID:       cfg.MQTT.ClientID,
			Username:       cfg.MQTT.Username,
			Password:       cfg.MQTT.Password,
			QoS:            byte(cfg.MQTT.QoS),
			DataTopic:      cfg.MQTT.DataTopic,
			AckTopic:       cfg.MQTT.AckTopic,
			KeepAlive:      time.Duration(cfg.MQTT.KeepAliveSec) * time.Second,
			ConnectTimeout: time.Duration(cfg.MQTT.ConnectTimeoutMs) * time.Millisecond,
			PublishTimeout: time.Duration(cfg.MQTT.PublishTimeoutMs) * time.Millisecond,
			AckTimeout:     time.Duration(cfg.MQTT.AckTimeoutMs) * time.Millisecond,
			TLS:            tlsConfig,
		}, log)

		connectCtx, cancel := context.WithTimeout(ctx, time.Duration(cfg.MQTT.ConnectTimeoutMs)*time.Millisecond)
		defer cancel()
		if err := adapter.Connect(connectCtx); err != nil {
			// Not fatal — see the doc comment above. paho's own
			// SetConnectRetry keeps trying in the background regardless of
			// whether we waited for it here, and RunReconnectWatchdog
			// (started below) will force a reconnect if that stalls too.
			log.Warn("initial mqtt connect failed, gateway is starting anyway and will keep retrying in the background", "broker", cfg.MQTT.BrokerURL, "error", err)
		}

		// ctx (not connectCtx, which is cancelled right after Connect
		// returns) is the gateway's top-level shutdown context — the
		// watchdog runs for the process's lifetime, same as every other
		// long-running loop (see HANDOFF.md).
		go adapter.RunReconnectWatchdog(ctx)

		return adapter, func() { adapter.Disconnect(250) }, nil

	default:
		return nil, nil, fmt.Errorf("unknown forwarder transport %q (want \"http\" or \"mqtt\")", cfg.Forwarder.Transport)
	}
}
