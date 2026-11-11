// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"encoding/json"
	"fmt"
	"math"
	"time"

	paho "github.com/eclipse/paho.mqtt.golang"
	"github.com/spf13/cobra"

	"github.com/nereidai/nereidlink/internal/config"
	"github.com/nereidai/nereidlink/internal/mqttapi"
)

// simulatorDisconnectQuiesce is the wait in milliseconds for in-flight
// messages during clean MQTT disconnection.
const simulatorDisconnectQuiesce = 500

// commandFrame is the JSON downlink frame handled by --respond.
type commandFrame struct {
	ID     string          `json:"id"`
	Name   string          `json:"name"`
	Params json.RawMessage `json:"params"`
}

// simulateCommand returns the development device simulator command.
//
// The simulator connects to the MQTT ingress with seeded token credentials
// and publishes sine-wave telemetry at a fixed interval. It is the seed of
// the first-class device simulator product used in load tests later.
func simulateCommand() *cobra.Command {
	var interval time.Duration
	var count int
	var hold time.Duration
	var respond bool

	cmd := &cobra.Command{
		Use:   "simulate",
		Short: "Publish simulated telemetry as the seeded device",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := requireConfig()
			if err != nil {
				return err
			}
			return runSimulate(cfg, interval, count, hold, respond)
		},
	}
	cmd.Flags().DurationVar(&interval, "interval", time.Second, "publish interval")
	cmd.Flags().IntVar(&count, "count", 0, "number of frames to publish, 0 means forever")
	cmd.Flags().DurationVar(&hold, "hold", 0, "keep the connection open for this duration after publishing")
	cmd.Flags().BoolVar(&respond, "respond", false, "subscribe to commands and publish a response for each one")
	return cmd
}

// runSimulate connects, publishes count frames (or forever), optionally
// holds the connection open, and then disconnects cleanly.
func runSimulate(cfg *config.Config, interval time.Duration, count int, hold time.Duration, respond bool) error {
	broker := "mqtt://" + trimAddress(cfg.IngestMQTT.Listen)
	opts := paho.NewClientOptions().
		AddBroker(broker).
		SetClientID("nl-simulator").
		SetUsername(cfg.Seed.DeviceToken).
		SetPassword("").
		// A force-disconnected device must not silently reconnect: the
		// simulator then observes the closed connection and exits.
		SetAutoReconnect(false)

	client := paho.NewClient(opts)
	if token := client.Connect(); token.Wait() && token.Error() != nil {
		return fmt.Errorf("simulator connect: %w", token.Error())
	}
	defer client.Disconnect(simulatorDisconnectQuiesce)

	if respond {
		subscribeCommands(client)
	}

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for i := 0; count == 0 || i < count; i++ {
		payload := fmt.Sprintf(`{"temperature": %.3f, "humidity": %.3f}`,
			20+5*math.Sin(float64(i)/10),
			50+10*math.Cos(float64(i)/20))
		token := client.Publish(mqttapi.TopicDeviceTelemetry, 1, false, payload)
		if token.Wait() && token.Error() != nil {
			return fmt.Errorf("simulator publish: %w", token.Error())
		}
		<-ticker.C
	}

	if respond && hold == 0 {
		hold = time.Hour
	}
	if hold > 0 {
		time.Sleep(hold)
	}
	return nil
}

// subscribeCommands receives downlink commands and publishes one JSON
// response per frame so the request-response path is testable end to end.
func subscribeCommands(client paho.Client) {
	handler := func(_ paho.Client, msg paho.Message) {
		var frame commandFrame
		if err := json.Unmarshal(msg.Payload(), &frame); err != nil {
			return
		}
		response := fmt.Sprintf(`{"ok": true, "command": %q}`, frame.Name)
		token := client.Publish(
			mqttapi.PrefixDeviceCommandResponse+frame.ID, 1, false, response)
		if token.Wait() && token.Error() != nil {
			return
		}
	}
	client.Subscribe(mqttapi.FilterDeviceCommands, 1, handler)
}
