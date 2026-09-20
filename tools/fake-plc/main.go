// fake-plc is a Modbus TCP device for testing: register N holds N.
//
// It stands in for the equipment, and it stays outside the image, because a
// PLC is outside the appliance in reality too. The appliance dials it, which
// is why it works through QEMU's user network with nothing forwarded: the
// guest reaches the host machine at 10.0.2.2, and a real deployment points
// MODBUS_DEVICE at a real device on the plant network instead.
//
// Unit 9 answers exception 11, so a refusal that came from the device can be
// told apart from one that came from the component or the host. Function
// code 3 is the only one implemented, because it is the only one the gateway
// can ask for — a simulator that could be written to would make the test
// weaker than the thing it tests.
//
//	go run ./tools/fake-plc            # 127.0.0.1:15020
//	go run ./tools/fake-plc -addr :502 # anywhere else
package main

import (
	"encoding/binary"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
)

const (
	readHolding      = 3
	exceptionNoReply = 11 // gateway target device failed to respond
	exceptionBadFunc = 1  // illegal function
	refusingUnit     = 9
	maxRegisters     = 125
)

func main() {
	addr := flag.String("addr", "127.0.0.1:15020", "address to listen on")
	flag.Parse()

	listener, err := net.Listen("tcp", *addr)
	if err != nil {
		log.Fatalf("fake-plc: %v", err)
	}
	fmt.Printf("fake-plc: listening on %s, register N holds N\n", listener.Addr())
	for {
		conn, err := listener.Accept()
		if err != nil {
			log.Printf("fake-plc: accept: %v", err)
			continue
		}
		go serve(conn)
	}
}

func serve(conn net.Conn) {
	defer conn.Close()
	for {
		reply, err := exchange(conn)
		if err != nil {
			if !errors.Is(err, io.EOF) && !errors.Is(err, io.ErrUnexpectedEOF) {
				log.Printf("fake-plc: %v", err)
			}
			return
		}
		if _, err := conn.Write(reply); err != nil {
			return
		}
	}
}

// exchange reads one request and builds its reply. The MBAP header is seven
// bytes — transaction, protocol, length, unit — followed by the PDU.
func exchange(conn net.Conn) ([]byte, error) {
	var head [8]byte
	if _, err := io.ReadFull(conn, head[:]); err != nil {
		return nil, err
	}
	transaction := binary.BigEndian.Uint16(head[0:2])
	length := binary.BigEndian.Uint16(head[4:6])
	unit := head[6]
	function := head[7]

	if length < 2 {
		return nil, fmt.Errorf("short request: length %d", length)
	}
	rest := make([]byte, length-2)
	if _, err := io.ReadFull(conn, rest); err != nil {
		return nil, err
	}

	var body []byte
	switch {
	case unit == refusingUnit:
		body = []byte{function | 0x80, exceptionNoReply}
	case function != readHolding || len(rest) < 4:
		body = []byte{function | 0x80, exceptionBadFunc}
	default:
		start := binary.BigEndian.Uint16(rest[0:2])
		count := binary.BigEndian.Uint16(rest[2:4])
		if count == 0 || count > maxRegisters {
			body = []byte{function | 0x80, exceptionBadFunc}
			break
		}
		values := make([]byte, 0, 2+int(count)*2)
		values = append(values, function, byte(int(count)*2))
		for i := 0; i < int(count); i++ {
			values = binary.BigEndian.AppendUint16(values, start+uint16(i))
		}
		body = values
	}

	reply := make([]byte, 0, 7+len(body))
	reply = binary.BigEndian.AppendUint16(reply, transaction)
	reply = binary.BigEndian.AppendUint16(reply, 0) // protocol id
	reply = binary.BigEndian.AppendUint16(reply, uint16(len(body)+1))
	reply = append(reply, unit)
	return append(reply, body...), nil
}
