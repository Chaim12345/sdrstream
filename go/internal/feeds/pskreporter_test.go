package feeds

import (
	"bufio"
	"context"
	"encoding/binary"
	"fmt"
	"net"
	"testing"
	"time"
)

func TestParsePSKSpot(t *testing.T) {
	payload := []byte(`{"sc":"4x1rf","rc":"F59706","sl":"KM72jb","rl":"JN47qb","f":14074888,"md":"FT8","rp":-12,"t":1792615200,"b":"20m"}`)
	it, ok := ParsePSKSpot(payload, time.Now())
	if !ok {
		t.Fatal("no item")
	}
	if it.Label != "4X1RF" || it.Detail == "" || it.Time.Unix() != 1792615200 {
		t.Fatalf("bad spot: %+v", it)
	}
	if !it.HasPos {
		t.Fatalf("locator did not resolve: %+v", it)
	}
}

func TestMaidenhead(t *testing.T) {
	lat, lon, ok := maidenheadToLatLon("KM72")
	if !ok || lat != 32.5 || lon != 35 {
		t.Fatalf("KM72 resolved wrong: %v %v", lat, lon)
	}
	if _, _, ok := maidenheadToLatLon("K"); ok {
		t.Fatal("short locator should fail")
	}
}

// fakeMQTT speaks just enough MQTT 3.1.1 to test the subscriber.
func fakeMQTT(t *testing.T, ln net.Listener, spotPayload []byte) {
	conn, err := ln.Accept()
	if err != nil {
		return
	}
	defer conn.Close()
	r := bufio.NewReader(conn)
	readPkt := func() (byte, []byte) {
		typ, err := r.ReadByte()
		if err != nil {
			t.Errorf("read type: %v", err)
			return 0, nil
		}
		length := 0
		shift := uint(0)
		for {
			b, _ := r.ReadByte()
			length |= int(b&0x7F) << shift
			if b&0x80 == 0 {
				break
			}
			shift += 7
		}
		payload := make([]byte, length)
		if _, err := r.Read(payload); err != nil {
			t.Errorf("read payload: %v", err)
		}
		return typ, payload
	}
	writePkt := func(typ byte, payload []byte) {
		head := []byte{typ}
		head = append(head, mqttRemainingLength(len(payload))...)
		conn.Write(append(head, payload...))
	}
	if typ, _ := readPkt(); typ != 0x10 {
		t.Errorf("want CONNECT, got %#x", typ)
		return
	}
	writePkt(0x20, []byte{0, 0}) // CONNACK
	typ, sub := readPkt()
	if typ != 0x82 {
		t.Errorf("want SUBSCRIBE, got %#x", typ)
		return
	}
	topicLen := int(binary.BigEndian.Uint16(sub[2:4]))
	topic := string(sub[4 : 4+topicLen])
	if topic == "" {
		t.Errorf("empty subscribe topic")
	}
	writePkt(0x90, []byte{0, 1, 0}) // SUBACK
	// One PUBLISH with topic + payload.
	topicB := []byte("pskr/filter/v2/20m/FT8/4X1RF/F59706/KM72/JN47/Israel/France")
	body := []byte{byte(len(topicB) >> 8), byte(len(topicB))}
	body = append(body, topicB...)
	body = append(body, spotPayload...)
	writePkt(0x30, body)
	time.Sleep(300 * time.Millisecond)
	fmt.Println("fake mqtt done")
}

func TestPSKReporterRun(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	payload := []byte(`{"sc":"4X1RF","rc":"F59706","sl":"KM72jb","rl":"JN47qb","f":14074888,"md":"FT8","rp":-12,"t":1792615200,"b":"20m"}`)
	go fakeMQTT(t, ln, payload)
	src := &PSKReporter{Server: ln.Addr().String()}
	ctx, cancel := context.WithCancel(context.Background())
	var got []Item
	done := make(chan error, 1)
	go func() { done <- src.Run(ctx, func(it Item) { got = append(got, it) }) }()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) && len(got) == 0 {
		time.Sleep(20 * time.Millisecond)
	}
	cancel()
	<-done
	if len(got) != 1 || got[0].Label != "4X1RF" {
		t.Fatalf("want one 4X1RF spot, got %+v", got)
	}
}
