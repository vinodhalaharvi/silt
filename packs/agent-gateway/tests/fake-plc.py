"""A Modbus TCP device for tests: register N holds N.

Unit 9 answers exception 11, so a refusal that came from the device can be
told apart from one that came from the component or from the host.

Function code 3 is the only one implemented, because it is the only one the
gateway can ask for.
"""

import socket
import struct
import sys

HOST = "127.0.0.1"
PORT = int(sys.argv[1]) if len(sys.argv) > 1 else 15020
READ_HOLDING = 3


def main():
    server = socket.socket()
    server.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEADDR, 1)
    server.bind((HOST, PORT))
    server.listen(5)
    print(f"fake-plc: listening on {HOST}:{PORT}", flush=True)
    while True:
        conn, _ = server.accept()
        try:
            req = conn.recv(256)
            if len(req) < 12:
                continue
            tid, _pid, _len, unit, func = struct.unpack(">HHHBB", req[:8])
            addr, count = struct.unpack(">HH", req[8:12])
            if unit == 9:
                body = struct.pack(">BB", func | 0x80, 11)
            elif func != READ_HOLDING:
                body = struct.pack(">BB", func | 0x80, 1)
            else:
                values = b"".join(
                    struct.pack(">H", (addr + i) & 0xFFFF) for i in range(count)
                )
                body = struct.pack(">BB", func, len(values)) + values
            conn.sendall(struct.pack(">HHHB", tid, 0, len(body) + 1, unit) + body)
        finally:
            conn.close()


if __name__ == "__main__":
    main()
