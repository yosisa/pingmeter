package main

import (
	"bytes"
	"flag"
	"io/ioutil"
	"log"
	"net"
	"net/http"
	"os"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/tatsushid/go-fastping"
)

var (
	pingInterval = flag.Duration("interval", 10*time.Second, "Ping interval")
	pingTimeout  = flag.Duration("timeout", 5*time.Second, "Ping timeout")
	listen       = flag.String("listen", ":9010", "Listen address for prometheus")

	pingMetrics *metrics
)

type metrics struct {
	ok    *prometheus.CounterVec
	ng    *prometheus.CounterVec
	total *prometheus.CounterVec
	rtt   *prometheus.GaugeVec
}

func newMetrics() *metrics {
	m := &metrics{
		total: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Name: "pingmeter_count_total",
				Help: "Number of checks",
			},
			[]string{"host"},
		),
		ok: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Name: "pingmeter_count_ok",
				Help: "Number of successes",
			},
			[]string{"host"},
		),
		ng: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Name: "pingmeter_count_ng",
				Help: "Number of failures",
			},
			[]string{"host"},
		),
		rtt: prometheus.NewGaugeVec(
			prometheus.GaugeOpts{
				Name: "pingmeter_rtt_ms",
				Help: "RTT to each host",
			},
			[]string{"host"},
		),
	}
	prometheus.MustRegister(m.total)
	prometheus.MustRegister(m.ok)
	prometheus.MustRegister(m.ng)
	prometheus.MustRegister(m.rtt)
	return m
}

func (m *metrics) update(host string, ok bool, rtt time.Duration) {
	m.total.WithLabelValues(host).Inc()
	if ok {
		m.ok.WithLabelValues(host).Inc()
		m.rtt.WithLabelValues(host).Set(rtt.Seconds() * 1000)
	} else {
		m.ng.WithLabelValues(host).Inc()
		m.rtt.WithLabelValues(host).Set(0)
	}
}

type result struct {
	host string
	ok   bool
	rtt  time.Duration
}

type targetList struct {
	items []string
	path  string
	mtime time.Time
}

func (t *targetList) read() {
	fi, err := os.Stat(t.path)
	if err != nil {
		log.Print(err)
		return
	}
	mtime := fi.ModTime()
	if !mtime.After(t.mtime) {
		return
	}
	t.mtime = mtime

	b, err := ioutil.ReadFile(t.path)
	if err != nil {
		log.Print(err)
		return
	}
	t.items = t.items[:0]
	for _, item := range bytes.Split(b, []byte{'\n'}) {
		if len(item) > 0 {
			t.items = append(t.items, string(item))
		}
	}
	log.Print("target list updated")
}

func pingLoop(path string) {
	t := &targetList{path: path}
	t.read()
	ping(t.items)
	for _ = range time.Tick(*pingInterval) {
		t.read()
		ping(t.items)
	}
}

func ping(hosts []string) error {
	results := make(map[string]*result)
	p := fastping.NewPinger()
	p.MaxRTT = *pingTimeout
	p.OnRecv = func(addr *net.IPAddr, rtt time.Duration) {
		if result, ok := results[addr.String()]; ok {
			result.ok = true
			result.rtt = rtt
		}
	}

	for _, host := range hosts {
		ra, err := net.ResolveIPAddr("ip:icmp", host)
		results[ra.String()] = &result{host: host}
		if err == nil {
			p.AddIPAddr(ra)
		}
	}
	defer func() {
		for _, r := range results {
			pingMetrics.update(r.host, r.ok, r.rtt)
		}
	}()
	return p.Run()
}

func main() {
	flag.Parse()
	go pingLoop(os.Args[len(os.Args)-1])
	http.Handle("/metrics", prometheus.Handler())
	http.ListenAndServe(*listen, nil)
}

func init() {
	pingMetrics = newMetrics()
}
