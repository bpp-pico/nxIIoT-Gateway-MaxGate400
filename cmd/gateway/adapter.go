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
// shutdown. For "mqtt" this also performs the initial Connect — a failure
// here is fatal at startup rather than silently falling back, since a
// gateway silently not forwarding is worse than one that fails loudly.
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
			return nil, nil, fmt.Errorf("connect to mqtt broker %s: %w", cfg.MQTT.BrokerURL, err)
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
