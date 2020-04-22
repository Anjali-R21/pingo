package main

import (
	"flag"
	"fmt"
	"os"
	"os/signal"
	"time"
	"net"

	"github.com/Anjali-R21/pingo"
)

var usage = `
Usage:
    ping [-c count] [-t timeout] host
Options:
	-c number of ping requests sent
	-t continuous requests till "<t>s" in seconds
`
func Resolve(host string) (*net.IPAddr, error){
    dst, err := net.ResolveIPAddr("ip", host)
    if err != nil {
        panic(err)
        return nil, err
    }
    return dst, err
}

func isIPv4(ip net.IP) bool {
	return len(ip.To4()) == net.IPv4len
}

func main() {
	timeout := flag.Duration("t", time.Second*100000, "")
	interval := flag.Duration("i", time.Second, "")
	count := flag.Int("c", -1, "")

	flag.Usage = func() {
		fmt.Printf(usage)
	}
	flag.Parse()

	if flag.NArg() == 0 || flag.NArg() > 1 {
		flag.Usage()
		return
	}

	host := flag.Arg(0)
	ips, err := net.LookupIP(host)
	if err != nil {
		panic(err)
		os.Exit(1)
	}
	addr, err := Resolve(host)
	if err != nil {
		fmt.Printf("ERROR: %s\n", err.Error())
		return
	}

	var source string
	for _, ip := range ips {
		if isIPv4(ip) == true {
			source = ip.String()
			break
		}
	}


	pinger, err := pingo.InitRequest(source, addr, *timeout,  os.Getpid() & 0xffff, true, *count)
	if err != nil {
		fmt.Printf("ERROR: %s\n", err.Error())
		return
	}

	// listen for ctrl-C signal
	c := make(chan os.Signal, 1)
	signal.Notify(c, os.Interrupt)
	go func() {
		for _ = range c {
			pinger.Stop()
		}
	}()

	pinger.OnRecv = func(pkt *pingo.Packet) {
		fmt.Printf("%d bytes from %s: icmp_seq=%d RTT=%v ttl=%v\n",
			pkt.Nbytes, pkt.IPAddr, pkt.Seq, pkt.Rtt, pkt.Ttl)
	}

	pinger.Count = *count
	pinger.Interval = *interval
	pinger.Timeout = *timeout

	fmt.Printf("PING %s (%s):\n", pinger.GetAddr(), pinger.GetHostname())
	pinger.Start()
}
