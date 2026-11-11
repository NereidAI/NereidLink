// Copyright (C) 2026 YIQISOFT
// SPDX-License-Identifier: Apache-2.0

// Command coap_device_sim simulates a CoAP device: it publishes telemetry
// frames to /telemetry and continuously long-polls /commands, responding to
// every command through /cmdres/<id>. It is the CoAP counterpart of the
// MQTT and HTTP device simulators. The --eps flag sets the telemetry
// frames per second (e.g. --eps 20 --count 1000).

package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log"
	"math"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/plgd-dev/go-coap/v3/message"
	"github.com/plgd-dev/go-coap/v3/options"
	"github.com/plgd-dev/go-coap/v3/udp"
	udpclient "github.com/plgd-dev/go-coap/v3/udp/client"
)

// commandFrame is one command returned by /commands.
type commandFrame struct {
	ID     string          `json:"id"`
	Name   string          `json:"name"`
	Params json.RawMessage `json:"params"`
}

// commandsResponse wraps the optional command.
type commandsResponse struct {
	Command *commandFrame `json:"command"`
}

// telemetryResponse reports the published point count.
type telemetryResponse struct {
	Published int `json:"published"`
}

func main() {
	host := flag.String("host", "127.0.0.1", "CoAP server host")
	port := flag.Int("port", 5683, "CoAP server port")
	token := flag.String("token", "seed-device-token", "device access token")
	interval := flag.Duration("interval", 3*time.Second, "telemetry interval")
	eps := flag.Float64("eps", 0, "telemetry frames per second, e.g. 0.5 or 20; mutually exclusive with --interval")
	count := flag.Int("count", 0, "telemetry frames to send, 0 means forever")
	hold := flag.Duration("hold", 30*time.Second, "listen duration when count>0")
	payload := flag.String("payload", "", "custom JSON payload, supports ${i}, ${ts}, ${uuid}")
	payloadFile := flag.String("payload-file", "", "JSON file: object repeated or array cycled per frame")
	flag.Parse()

	if *eps < 0 {
		log.Fatal("--eps must be positive")
	}
	if *eps > 0 {
		intervalSet := false
		flag.Visit(func(f *flag.Flag) {
			if f.Name == "interval" {
				intervalSet = true
			}
		})
		if intervalSet {
			log.Fatal("--eps and --interval are mutually exclusive")
		}
		*interval = time.Duration(float64(time.Second) / *eps)
	}

	payloads, err := newPayloadSource(*payload, *payloadFile)
	if err != nil {
		log.Fatal(err)
	}

	// NSTART > 1 lets an in-flight long-poll GET coexist with telemetry
	// POSTs on the same connection; the RFC default of 1 would block POSTs
	// until the poll returns. LimitClientParallelRequest must likewise be
	// raised: go-coap funnels every Get/Post through a single client-wide
	// semaphore whose default capacity is 1, independent of NSTART.
	co, err := udp.Dial(fmt.Sprintf("%s:%d", *host, *port),
		options.WithTransmission(8, 2*time.Second, 4),
		options.WithLimitClientParallelRequest(8))
	if err != nil {
		log.Fatalf("dial: %v", err)
	}
	defer func() { _ = co.Close() }()
	log.Printf("connected to %s:%d over CoAP with token %s...", *host, *port, (*token)[:min(6, len(*token))])
	log.Printf("[rate] target interval %s, EPS %.2f", interval.String(),
		float64(time.Second)/float64(*interval))

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	deadline := time.Time{}
	if *count > 0 {
		deadline = time.Now().Add(*hold)
	}

	frames := 0
	telemetryTick := time.NewTicker(*interval)
	defer telemetryTick.Stop()

	// The command poll loop runs in its own goroutine: a long-poll GET
	// blocks for up to the long-poll window and must not stall telemetry.
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		commandLoop(ctx, co, *token)
	}()

	for {
		select {
		case <-ctx.Done():
			wg.Wait()
			return
		case <-telemetryTick.C:
			sendTelemetry(ctx, co, *token, frames, payloads)
			frames++
			if *count > 0 && frames >= *count {
				telemetryTick.Stop()
			}
			if !deadline.IsZero() && time.Now().After(deadline) {
				stop()
				wg.Wait()
				return
			}
		}
	}
}

// commandLoop continuously long-polls and responds to commands until ctx
// is canceled.
func commandLoop(ctx context.Context, co *udpclient.Conn, token string) {
	for ctx.Err() == nil {
		pollAndRespond(ctx, co, token)
	}
}

// sendTelemetry publishes one frame and logs the accepted point count.
func sendTelemetry(ctx context.Context, co *udpclient.Conn, token string, frame int, payloads *payloadSource) {
	body := ""
	if payloads != nil {
		rendered, err := payloads.render(frame)
		if err != nil {
			log.Printf("[telemetry] render failed: %v", err)
			return
		}
		body = rendered
	} else {
		temp := 20 + 5*math.Sin(float64(frame)/3)
		hum := 60 - 10*math.Sin(float64(frame)/4)
		body = fmt.Sprintf(`{"temperature":%.3f,"humidity":%.3f}`, temp, hum)
	}

	path := "/telemetry"
	reqCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	resp, err := co.Post(reqCtx, path, message.AppJSON, strings.NewReader(body), tokenOption(token))
	if err != nil {
		log.Printf("[telemetry] failed: %v", err)
		return
	}
	raw, _ := resp.ReadBody()
	result := decodeTelemetry(raw)
	log.Printf("[telemetry] frame %d: %s -> %d points", frame+1, body, result.Published)
}

// pollAndRespond long-polls one command and responds when present.
func pollAndRespond(ctx context.Context, co *udpclient.Conn, token string) {
	path := "/commands"
	reqCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	resp, err := co.Get(reqCtx, path, tokenOption(token))
	if err != nil {
		if !errors.Is(err, context.DeadlineExceeded) {
			log.Printf("[commands] poll failed: %v", err)
			time.Sleep(time.Second)
		}
		return
	}
	result := decodeCommands(readBody(resp))
	if result.Command == nil {
		return
	}
	log.Printf("[command] %s %s", result.Command.Name, string(result.Command.Params))
	respond(ctx, co, token, result.Command)
}

// respond posts a command response.
func respond(ctx context.Context, co *udpclient.Conn, token string, cmd *commandFrame) {
	body := fmt.Sprintf(`{"ok":true,"received":%q}`, cmd.Name)
	path := "/cmdres/" + cmd.ID
	reqCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if _, err := co.Post(reqCtx, path, message.AppJSON, strings.NewReader(body), tokenOption(token)); err != nil {
		log.Printf("[response] failed: %v", err)
		return
	}
	log.Printf("[response] posted for %s", cmd.ID)
}

// readBody extracts the full raw body of a response message.
func readBody(resp respMessage) []byte {
	data, err := resp.ReadBody()
	if err != nil {
		return nil
	}
	return data
}

// decodeTelemetry unmarshals a telemetry response body.
func decodeTelemetry(data []byte) telemetryResponse {
	var value telemetryResponse
	_ = json.Unmarshal(data, &value)
	return value
}

// decodeCommands unmarshals a commands response body.
func decodeCommands(data []byte) commandsResponse {
	var value commandsResponse
	_ = json.Unmarshal(data, &value)
	return value
}

// tokenOption builds the URI-Query option carrying the device token.
// Embedding the token in the path string does not work: go-coap SetPath
// encodes everything after '?' as a Uri-Path segment.
func tokenOption(token string) message.Option {
	return message.Option{ID: message.URIQuery, Value: []byte("token=" + token)}
}

// respMessage is the pool response type exposing ReadBody.
type respMessage interface {
	ReadBody() ([]byte, error)
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
