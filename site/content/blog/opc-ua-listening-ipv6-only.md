---
title: An OPC UA server that listens only on IPv6, and refuses every client
description: open62541 picks its listening addresses with AI_ADDRCONFIG. Start
  it before DHCP finishes and it binds IPv6 only, so every IPv4 client gets
  connection refused while the process looks healthy.
kind: article
date: 2026-09-20
---

# An OPC UA server that listens only on IPv6

The symptom is maddening because everything looks right. The server is
running:

```
# ps | grep opcua
  152 root     /usr/bin/silt-opcuad
```

The port is open:

```
# netstat -ltn | grep 4840
tcp        0      0 :::4840      :::*      LISTEN
```

And every client is refused:

```
ConnectionRefusedError: [Errno 61] Connect call failed ('192.168.1.50', 4840)
```

The answer is in that `netstat` line, and it is easy to read past. There is
one entry, `:::4840`, which is IPv6. There is no `0.0.0.0:4840`. The server is
not listening on IPv4 at all, so an IPv4 client is refused by the kernel
before the server ever sees it.

## Why one socket and not two

open62541 asks the system which addresses to listen on rather than binding to
"anything" directly. In its TCP layer:

```c
ai_hints.ai_flags = AI_PASSIVE;
#ifdef AI_ADDRCONFIG
ai_hints.ai_flags |= AI_ADDRCONFIG;
#endif
```

`AI_ADDRCONFIG` means: only return IPv4 addresses if this machine has a
configured, non-loopback IPv4 address, and likewise for IPv6. It exists to
stop programs opening sockets for a protocol family the machine cannot use.

Now consider a board booting. The link comes up and the kernel gives the
interface an IPv6 link-local address immediately, because that costs no
negotiation. The IPv4 address arrives later, when the DHCP exchange finishes,
which might be one second or fifteen. If the server starts in between,
`getaddrinfo` reports IPv6 only, so that is the only socket it creates.

That socket is deliberately IPv6-only: open62541 sets `IPV6_V6ONLY` on it, so
it will not accept IPv4 connections the way a dual-stack socket would. The
server then runs normally forever, with no error to report. It asked what
existed, it was told, and it obliged.

Restart it by hand once the network is up and it binds both:

```
tcp        0      0 0.0.0.0:4840      0.0.0.0:*      LISTEN
tcp        0      0 :::4840           :::*           LISTEN
```

Which is a good confirmation and a bad fix.

## The fix

Wait for an IPv4 address before starting the server. In a sysv init script:

```sh
have_ipv4() {
    ip -4 addr show 2>/dev/null | grep "inet " | grep -qv "127\.0\.0\.1"
}

i=0
while [ "$i" -lt 60 ] && ! have_ipv4; do
    sleep 1
    i=$((i + 1))
done
```

Do that in a background subshell, so a board with no network at all still
finishes booting rather than stalling for a minute at that line.

Under systemd it is a dependency rather than a loop:

```
Wants=network-online.target
After=network-online.target
```

Note that `network.target` is not enough. It means the networking service has
started, not that an address exists. `network-online.target` is the one that
waits for configuration to complete, and it needs the matching wait service
enabled (`systemd-networkd-wait-online`, or whichever your distribution uses).

## How to recognise this class of bug

Three signs together are the tell:

- the process is running and logs nothing unusual,
- `netstat -ltn` shows `:::port` and no `0.0.0.0:port`,
- restarting it by hand, later, makes it work.

Anything that resolves listening addresses at startup can do this. libmodbus,
by contrast, binds `INADDR_ANY` directly when you pass no host, so a Modbus
server on the same board is unaffected by the same race. That asymmetry is a
useful diagnostic in itself: if one of your servers is reachable and another
is not, look at how each one chose its socket.
