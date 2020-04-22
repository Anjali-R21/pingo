package pingo

import (
    "time"
    "fmt"
    "net"
    "bytes"
    "sync"
    "syscall"

    "golang.org/x/net/icmp"
    "golang.org/x/net/ipv4"
    "golang.org/x/net/ipv6"

)

type packet struct {
	bytes  []byte
	nbytes int
	ttl    int
}

func bytesToTime(b []byte) time.Time {
	var nsec int64
	for i := uint8(0); i < 8; i++ {
		nsec += int64(b[i]) << ((7 - i) * 8)
	}
	return time.Unix(nsec/1000000000, nsec%1000000000)
}

func timeToBytes(t time.Time) []byte {
	nsec := t.UnixNano()
	b := make([]byte, 8)
	for i := uint8(0); i < 8; i++ {
		b[i] = byte((nsec >> ((7 - i) * 8)) & 0xff)
	}
	return b
}


type Packet struct {
	Rtt time.Duration
	IPAddr *net.IPAddr
	Addr string
	Nbytes int
    Seq int
	Ttl int
}

const (
	timeSliceLength  = 8
	protocolICMP     = 1
	protocolIPv6ICMP = 58
)

type Request struct {
    ipaddr      *net.IPAddr
    hostname    string
    ipv4        bool
    id          int

    Interval    time.Duration
    Timeout     time.Duration
    Count       int

    handle      chan bool
    sequence    int
    rtts        []time.Duration

    Size        int
    PacketsSent int
    PacketsRecv int
    OnRecv      func(*Packet)
}

func InitRequest(host string, addr *net.IPAddr, timeout time.Duration, pid int, ipv4 bool, count int) (*Request, error){

    return &Request{
		ipaddr:   addr,
		hostname:  host,
		Interval: time.Second,
		Timeout:  time.Second * timeout,
		Count:    count,
		id:       pid,
		ipv4:     ipv4,
		Size:     timeSliceLength,
		handle:   make(chan bool),
	}, nil
}

func (req *Request )GetHostname() string {
	return req.hostname
}


func (req *Request )GetAddr() *net.IPAddr {
	return req.ipaddr
}

func (req *Request) Stop() {
	close(req.handle)
}

func (req *Request)Start() {
    var conn *icmp.PacketConn
	if req.ipv4 {
		if conn = req.listen("ip4:icmp"); conn == nil {
			return
		}
		conn.IPv4PacketConn().SetControlMessage(ipv4.FlagTTL, true)
	} else {
		if conn = req.listen("ip6:58"); conn == nil {
			return
		}
		conn.IPv6PacketConn().SetControlMessage(ipv6.FlagHopLimit, true)
	}
	defer conn.Close()

	var wg sync.WaitGroup
	recv := make(chan *packet, 5)
	defer close(recv)
	wg.Add(1)
	go req.recv(conn, recv, &wg)

	err := req.send(conn)
	if err != nil {
		fmt.Println(err.Error())
	}

	timeout := time.NewTicker(req.Timeout)
	defer timeout.Stop()
	interval := time.NewTicker(req.Interval)
	defer interval.Stop()

	for {
		select {
		case <-req.handle:
			wg.Wait()
			return
		case <-timeout.C:
			close(req.handle)
			wg.Wait()
			return
		case <-interval.C:
			if req.Count > 0 && req.PacketsSent >= req.Count {
				continue
			}
			err = req.send(conn)
			if err != nil {
				fmt.Println("FATAL: ", err.Error())
			}
		case r := <-recv:
			err := req.parsePacket(r)
			if err != nil {
				fmt.Println("FATAL: ", err.Error())
			}
		}
		if req.Count > 0 && req.PacketsRecv >= req.Count {
			close(req.handle)
			wg.Wait()
			return
		}
	}

}

func (req *Request) listen(proto string) *icmp.PacketConn {

    conn, err := icmp.ListenPacket(proto, req.hostname)
	if err != nil {
		panic(err);
		close(req.handle)
		return nil
	}
	return conn
}

func (req *Request)send( conn *icmp.PacketConn) error{
    var typ icmp.Type
	if req.ipv4 {
		typ = ipv4.ICMPTypeEcho
	} else {
		typ = ipv6.ICMPTypeEchoRequest
	}

    var dst net.Addr = req.ipaddr
	t := timeToBytes(time.Now())
	if remainSize := req.Size - timeSliceLength; remainSize > 0 {
		t = append(t, bytes.Repeat([]byte{1}, remainSize)...)
	}

	body := &icmp.Echo{
		ID:   req.id,
		Seq:  req.sequence,
		Data: t,
	}

	msg := &icmp.Message{
		Type: typ,
		Code: 0,
		Body: body,
	}

	msgBytes, err := msg.Marshal(nil)
	if err != nil {
		return err
	}

	for {
		if _, err := conn.WriteTo(msgBytes, dst); err != nil {
			if neterr, ok := err.(*net.OpError); ok {
				if neterr.Err == syscall.ENOBUFS {
					continue
				}
			}
		}
		req.PacketsSent++
		req.sequence++
		break
	}

	return nil
}

func (req *Request) recv( conn *icmp.PacketConn, recv chan<- *packet, wg *sync.WaitGroup) {
	defer wg.Done()
	for {
		select {
		case <-req.handle:
			return
		default:
			bytes := make([]byte, 512)
			conn.SetReadDeadline(time.Now().Add(time.Millisecond * 100))
			var n, ttl int
			var err error
			if req.ipv4 {
				var cm *ipv4.ControlMessage
				n, cm, _, err = conn.IPv4PacketConn().ReadFrom(bytes)
				if cm != nil {
					ttl = cm.TTL
				}
			} else {
				var cm *ipv6.ControlMessage
				n, cm, _, err = conn.IPv6PacketConn().ReadFrom(bytes)
				if cm != nil {
					ttl = cm.HopLimit
				}
			}
			if err != nil {
				if neterr, ok := err.(*net.OpError); ok {
					if neterr.Timeout() {
						// Read timeout
						continue
					} else {
						close(req.handle)
						return
					}
				}
			}

			recv <- &packet{bytes: bytes, nbytes: n, ttl: ttl}
		}
	}
}

func (req *Request) parsePacket(recv *packet) error{

    receivedAt := time.Now()
    var proto int
    if req.ipv4 {
        proto = protocolICMP
    } else {
        proto = protocolIPv6ICMP
    }

    var m *icmp.Message
    var err error
    if m, err = icmp.ParseMessage(proto, recv.bytes); err != nil {
        return fmt.Errorf("error parsing icmp message: %s", err.Error())
    }

    // Not an echo reply
    if m.Type != ipv4.ICMPTypeEchoReply && m.Type != ipv6.ICMPTypeEchoReply {
        return nil
    }

    outPkt := &Packet{
        Nbytes: recv.nbytes,
        IPAddr: req.ipaddr,
        Addr:   req.hostname,
        Ttl:    recv.ttl,
    }

    switch pkt := m.Body.(type) {
    case *icmp.Echo:
        // NEEDS ROOT PRIVILEGES
        if pkt.ID != req.id {
            return nil
        }

        timestamp := bytesToTime(pkt.Data[:timeSliceLength])

        outPkt.Rtt = receivedAt.Sub(timestamp)
        outPkt.Seq = pkt.Seq
        req.PacketsRecv++
    default:
        return fmt.Errorf("invalid ICMP echo reply; type: '%T', '%v'", pkt, pkt)
    }

    req.rtts = append(req.rtts, outPkt.Rtt)
    handler := req.OnRecv
    if handler != nil {
        handler(outPkt)
    }

    return nil


}

// source code referred from https://github.com/sparrc/go-ping.git
