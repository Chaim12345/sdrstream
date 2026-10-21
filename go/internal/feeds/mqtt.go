package feeds

// Minimal MQTT 3.1.1 subscriber: enough of the protocol to attach to the
// PSK Reporter live spot feed (mqtt.pskreporter.info) without pulling in a
// dependency. CONNECT, SUBSCRIBE at QoS 0, PUBLISH fan-in, keepalive pings.

import (
	"bufio"
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"time"
)

type mqttClient struct {
	conn net.Conn
	r    *bufio.Reader
}

func (c *mqttClient) Close() error { return c.conn.Close() }

func mqttDial(ctx context.Context, addr, clientID string, keepalive time.Duration) (*mqttClient, error) {
	d := &net.Dialer{Timeout: 20 * time.Second}
	conn, err := d.DialContext(ctx, "tcp", addr)
	if err != nil {
		return nil, err
	}
	c := &mqttClient{conn: conn, r: bufio.NewReader(conn)}

	var pkt []byte
	pkt = append(pkt, 0x00, 0x04, 'M', 'Q', 'T', 'T', 0x04, 0x02) // v3.1.1, clean session
	pkt = append(pkt, byte(int(keepalive/time.Second)>>8), byte(int(keepalive/time.Second)))
	pkt = append(pkt, 0x00, byte(len(clientID)))
	pkt = append(pkt, clientID...)
	if err := c.write(0x10, pkt); err != nil {
		conn.Close()
		return nil, err
	}
	typ, payload, err := c.read()
	if err != nil {
		conn.Close()
		return nil, err
	}
	if typ != 0x20 || len(payload) != 2 || payload[1] != 0x00 {
		conn.Close()
		return nil, fmt.Errorf("mqtt: CONNACK refused")
	}
	return c, nil
}

func (c *mqttClient) subscribe(topic string) error {
	payload := []byte{0x00, 0x01} // packet id
	payload = append(payload, 0x00, byte(len(topic)))
	payload = append(payload, topic...)
	payload = append(payload, 0x00) // QoS 0
	if err := c.write(0x82, payload); err != nil {
		return err
	}
	typ, _, err := c.read()
	if err != nil {
		return err
	}
	if typ != 0x90 {
		return fmt.Errorf("mqtt: no SUBACK (type %#x)", typ)
	}
	return nil
}

func (c *mqttClient) ping() error { return c.write(0xC0, nil) }

func (c *mqttClient) write(typ byte, payload []byte) error {
	head := []byte{typ}
	head = append(head, mqttRemainingLength(len(payload))...)
	if err := c.conn.SetWriteDeadline(time.Now().Add(15 * time.Second)); err != nil {
		return err
	}
	_, err := c.conn.Write(append(head, payload...))
	return err
}

func mqttRemainingLength(n int) []byte {
	var out []byte
	for {
		d := byte(n % 128)
		n /= 128
		if n > 0 {
			d |= 0x80
		}
		out = append(out, d)
		if n == 0 {
			break
		}
	}
	return out
}

// read returns the packet type byte and its payload.
func (c *mqttClient) read() (byte, []byte, error) {
	typ, err := c.r.ReadByte()
	if err != nil {
		return 0, nil, err
	}
	length := 0
	shift := uint(0)
	for i := 0; i < 4; i++ {
		b, err := c.r.ReadByte()
		if err != nil {
			return 0, nil, err
		}
		length |= int(b&0x7F) << shift
		if b&0x80 == 0 {
			break
		}
		shift += 7
	}
	if length > 4<<20 {
		return 0, nil, fmt.Errorf("mqtt: oversized packet (%d bytes)", length)
	}
	payload := make([]byte, length)
	if _, err := io.ReadFull(c.r, payload); err != nil {
		return 0, nil, err
	}
	return typ, payload, nil
}

// nextPublish blocks for the next QoS 0 PUBLISH, answering nothing else.
// PINGRESP and stray control packets are consumed silently.
func (c *mqttClient) nextPublish() (topic string, payload []byte, err error) {
	for {
		typ, body, err := c.read()
		if err != nil {
			return "", nil, err
		}
		if typ&0xF0 != 0x30 {
			continue
		}
		if len(body) < 2 {
			continue
		}
		tl := int(binary.BigEndian.Uint16(body))
		if len(body) < 2+tl {
			continue
		}
		return string(body[2 : 2+tl]), body[2+tl:], nil
	}
}
