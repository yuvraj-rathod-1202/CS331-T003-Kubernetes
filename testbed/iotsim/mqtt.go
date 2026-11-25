package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"strconv"
	"sync"
	"time"

	mqtt "github.com/eclipse/paho.mqtt.golang"
)

type pubConfig struct {
	Broker    string
	Group     string
	Clients   int
	Rate      float64
	QoS       int
	KeepAlive time.Duration
}

func parsePubFlags(args []string) (pubConfig, error) {
	var c pubConfig
	fs := flag.NewFlagSet("pub", flag.ContinueOnError)
	fs.StringVar(&c.Broker, "broker", "", "broker URL, e.g. tcp://host:1883")
	fs.StringVar(&c.Group, "group", "", "client group (id prefix and topic)")
	fs.IntVar(&c.Clients, "clients", 1, "number of clients")
	fs.Float64Var(&c.Rate, "rate", 1, "messages per second per client")
	fs.IntVar(&c.QoS, "qos", 1, "QoS (0 or 1)")
	fs.DurationVar(&c.KeepAlive, "keepalive", 15*time.Second, "MQTT keepalive")
	if err := fs.Parse(args); err != nil {
		return c, err
	}
	if c.Broker == "" || c.Group == "" {
		return c, fmt.Errorf("--broker and --group are required")
	}
	if c.Clients < 1 {
		return c, fmt.Errorf("--clients must be >= 1")
	}
	if c.Rate <= 0 {
		return c, fmt.Errorf("--rate must be > 0")
	}
	if c.QoS != 0 && c.QoS != 1 {
		return c, fmt.Errorf("--qos must be 0 or 1")
	}
	if c.KeepAlive <= 0 {
		return c, fmt.Errorf("--keepalive must be positive")
	}
	return c, nil
}

type subConfig struct {
	Broker string
	Topic  string
	ID     string
}

func parseSubFlags(args []string) (subConfig, error) {
	var c subConfig
	fs := flag.NewFlagSet("sub", flag.ContinueOnError)
	fs.StringVar(&c.Broker, "broker", "", "broker URL")
	fs.StringVar(&c.Topic, "topic", "iot/#", "topic filter")
	fs.StringVar(&c.ID, "id", "", "client id")
	if err := fs.Parse(args); err != nil {
		return c, err
	}
	if c.Broker == "" || c.ID == "" {
		return c, fmt.Errorf("--broker and --id are required")
	}
	return c, nil
}

func baseOpts(broker, id string, keepalive time.Duration, clean bool, out *lineWriter) *mqtt.ClientOptions {
	o := mqtt.NewClientOptions().
		AddBroker(broker).
		SetClientID(id).
		SetAutoReconnect(true).
		SetConnectRetry(true).
		SetConnectRetryInterval(time.Second).
		SetMaxReconnectInterval(2 * time.Second).
		SetCleanSession(clean).
		SetKeepAlive(keepalive).
		SetPingTimeout(5 * time.Second).
		SetWriteTimeout(5 * time.Second).
		SetOrderMatters(false).
		SetStore(mqtt.NewMemoryStore())
	o.SetConnectionLostHandler(func(_ mqtt.Client, err error) {
		out.emit("connlost", id, sanitizeReason(err.Error()))
	})
	return o
}

func runPub(ctx context.Context, out *lineWriter, c pubConfig) {
	var wg sync.WaitGroup
	var clients []mqtt.Client
	for i := 0; i < c.Clients; i++ {
		id := clientID(c.Group, i)
		o := baseOpts(c.Broker, id, c.KeepAlive, c.QoS == 0, out)
		o.SetOnConnectHandler(func(mqtt.Client) { out.emit("connected", id) })
		cl := mqtt.NewClient(o)
		clients = append(clients, cl)
		// Connect in background (ConnectRetry keeps trying); publishing starts on schedule regardless.
		cl.Connect()
		wg.Add(1)
		go func() {
			defer wg.Done()
			publishLoop(ctx, out, cl, id, c)
		}()
	}
	wg.Wait()
	for _, cl := range clients {
		cl.Disconnect(250)
	}
}

func publishLoop(ctx context.Context, out *lineWriter, cl mqtt.Client, id string, c pubConfig) {
	topic := "iot/" + c.Group + "/" + id
	period := time.Duration(float64(time.Second) / c.Rate)
	t := time.NewTicker(period)
	defer t.Stop()
	var seq uint64
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			seq++
			s := seq
			now := time.Now().UnixNano()
			out.emitAt(now, "pub", id, strconv.FormatUint(s, 10))
			tok := cl.Publish(topic, byte(c.QoS), false, makePayload(id, s, now, c.QoS))
			if c.QoS == 1 {
				go func() {
					tok.Wait()
					res := "ok"
					if tok.Error() != nil {
						res = "err"
					}
					out.emit("ack", id, strconv.FormatUint(s, 10), res)
				}()
			}
		}
	}
}

func runSub(ctx context.Context, out *lineWriter, c subConfig) {
	o := baseOpts(c.Broker, c.ID, 15*time.Second, true, out)
	handler := func(_ mqtt.Client, m mqtt.Message) {
		recv := time.Now().UnixNano()
		p, err := parsePayload(string(m.Payload()))
		if err != nil {
			fmt.Fprintf(os.Stderr, "ignoring malformed payload on %s: %v\n", m.Topic(), err)
			return
		}
		out.emitAt(recv, "recv", p.ClientID, strconv.FormatUint(p.Seq, 10), strconv.FormatInt(p.SentNs, 10), strconv.Itoa(p.QoS))
	}
	o.SetOnConnectHandler(func(cl mqtt.Client) {
		out.emit("connected", c.ID)
		cl.Subscribe(c.Topic, 1, handler)
	})
	cl := mqtt.NewClient(o)
	cl.Connect()
	<-ctx.Done()
	cl.Disconnect(250)
}
