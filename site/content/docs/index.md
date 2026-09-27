---
title: Configuration reference — Fieldbus Sim Appliance
description: How to describe a simulated device in one JSON file — tags,
  simulated behaviour, Modbus addresses, OPC UA nodes, CAN frame layout and
  MQTT topics.
kind: page
date: 2026-09-22
---

# Configuration reference

One file describes the device. The appliance validates it, publishes it into
shared memory, and every protocol daemon serves the same values from there.

The file lives at `/etc/silt-sim.json` on the board. You rarely edit it in
place; push it instead:

```
curl -s http://<board>:8080/config > device.json     # what is running now
curl -X POST --data-binary @device.json http://<board>:8080/config
```

## The shape of it

```json
{
  "device": "pump-1",
  "tags": [
    { "name": "temperature", "unit": "C", "type": "float",
      "sim": { "sine": { "min": 40, "max": 80, "period_s": 60 } },
      "modbus": { "input": 0, "scale": 10 },
      "opcua": "Temperature",
      "can": { "id": "0x100", "byte": 0, "len": 2, "scale": 10 } },

    { "name": "setpoint", "unit": "C", "type": "float",
      "writable": true, "initial": 60,
      "modbus": { "holding": 0, "scale": 10 },
      "opcua": "Setpoint" }
  ]
}
```

`device` names the device. It appears in OPC UA node ids and MQTT topics, so
two appliances on one network should not share a name.

## Tag fields

| Field | Meaning |
| --- | --- |
| `name` | The tag's name. Used for the MQTT topic and in `/tags`. |
| `type` | `float`, `int` or `bool`. Default `float`. |
| `unit` | Free text, shown as the OPC UA engineering unit. |
| `writable` | A client may set it; the simulator will not move it. |
| `signed` | Two's complement on the wire, so the value can be negative. |
| `initial` | Starting value, for writable tags. |
| `sim` | How the value moves. Leave it out for a writable tag. |

A tag is either simulated or writable, never both: two writers fighting over
one value is a bug, so the validator refuses it. Every tag needs at least one
protocol, otherwise nothing could read it.

## Simulated behaviour

| `sim` | Fields | What it does |
| --- | --- | --- |
| `sine` | `min`, `max`, `period_s` | Smooth cycle between the two. |
| `ramp` | `min`, `max`, `period_s` | Sawtooth, resets at the top. |
| `toggle` | `period_s` | 0 and 1, half the period each. |
| `counter` | `min`, `max`, `step`, `period_s` | Adds `step` each period, wraps. |
| `walk` | `min`, `max`, `step`, `period_s` | Random walk, stays in range. |

## Modbus

```json
"modbus": { "holding": 0, "scale": 10 }
```

One of `holding`, `input`, `coil` or `discrete`, followed by the address, plus
an optional `scale`.

A register carries `value × scale`, rounded. A temperature of 54.3 at scale 10
reads as 543. Coils and discrete inputs carry one bit, so those tags must be
`"type": "bool"`.

Addresses are zero-based, as the protocol defines them. Some clients number
from one, and some add 40001 or 30001; `mbpoll -0` matches what is written
here.

A simulated range that would not fit a 16-bit register at its scale is a
configuration error, caught when you push the file rather than met as a wrong
number later. An address no tag uses answers `ILLEGAL DATA ADDRESS`, the same
as a real device.

## OPC UA

```json
"opcua": "Temperature"
```

The name becomes a variable inside an object named after the device, so it is
addressed as `ns=1;s=<device>.<name>` — `ns=1;s=pump-1.Temperature` for the
example above. A tag with a unit carries it as an `EngineeringUnits` property
beside the value.

Writable tags are writable over OPC UA, and the new value is immediately what
Modbus, CAN and MQTT report.

## CAN

```json
"can": { "id": "0x100", "byte": 0, "len": 2, "scale": 10,
         "order": "big", "period_s": 0.1 }
```

| Field | Meaning |
| --- | --- |
| `id` | Frame id, decimal or `"0x100"`. Above 0x7FF uses the extended format. |
| `byte` | Where the signal starts in the frame, 0 to 7. |
| `len` | 1, 2 or 4 bytes. |
| `scale` | The wire carries `value × scale`. |
| `order` | `big` (Motorola, the default) or `little` (Intel). |
| `period_s` | How often the frame is sent; the shortest among a frame's tags wins. |

Several tags can share one id at different offsets: one frame carrying several
signals, the way a real device packs them. The frame's length is the highest
byte any of its tags reaches. Overlapping signals are a configuration error.

Received frames write the writable tags that frame carries, and nothing else.

## MQTT

Nothing to configure. Every tag is published as it changes:

```
silt/<device>/<tag>        retained, the value
silt/<device>/state        retained, every tag as one JSON object
silt/<device>/<tag>/set    write a writable tag
```

```
mosquitto_sub -h <board> -t 'silt/#' -v
mosquitto_pub -h <board> -t 'silt/pump-1/setpoint/set' -m 72.5
```

## HTTP

| Request | Does |
| --- | --- |
| `POST /config` | Validate and apply a device config. |
| `GET /config` | The config now running. |
| `GET /tags` | Every tag with its live value and its address on each protocol. |
| `GET /health` | Device name, config generation, tag count, uptime. |

A rejected config returns 400 with one line saying why, and changes nothing.

## On the board

```
/etc/silt-sim.json               the device
/etc/default/silt-can            which CAN interface, and its bitrate
/var/log/silt-*.log              what each daemon did
silt-simd -t -c FILE             check a config without applying it
/etc/init.d/S88silt-sim reload   re-read the config
```

## A real CAN bus

The image brings up `vcan0`, a virtual bus, so CAN works with no hardware. For
a real bus with an MCP2515 HAT, add the overlay to `config.txt` on the boot
partition and point the daemon at the interface:

```
# config.txt, on the FAT boot partition
dtoverlay=mcp2515-can0,oscillator=16000000,interrupt=25

# /etc/default/silt-can
IFACE=can0
BITRATE=500000
```

A bitrate that does not match the bus produces error frames and nothing else,
which is the first thing to check when a real bus stays silent.
