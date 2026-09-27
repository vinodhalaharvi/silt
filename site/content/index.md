---
title: Fieldbus Sim Appliance — a Modbus, OPC UA, CAN and MQTT device on a Raspberry Pi
description: Flash an SD card and get a simulated industrial device on your
  network in five minutes. Modbus TCP, OPC UA, CAN and MQTT, all serving one
  configuration you can change with curl.
kind: page
schema: product
date: 2026-09-22
---

# A device to test against, in five minutes

{{tagline}}

Writing an HMI screen, a SCADA tag list, a Node-RED flow or a protocol
gateway means having something to talk to. Usually that something is a PLC
you do not have yet, or one that is already in a machine you are not allowed
to stop.

This is that something: a Raspberry Pi image that boots into a simulated
device and answers on four protocols at once.

[Buy for ${{price}}]({{buy_url}}){.buy}

## What you get

- **Modbus TCP** on port 502: holding and input registers, coils and discrete
  inputs, scaled values, signed values, and a real `ILLEGAL DATA ADDRESS`
  exception for anything your config does not define.
- **OPC UA** on port 4840: an object named after your device, each tag a
  variable inside it with its engineering unit attached, browse, read, write
  and subscriptions.
- **CAN**: your tags packed into frames, several signals per frame, Motorola
  or Intel byte order, on a virtual bus that needs no hardware or on a real
  CAN HAT.
- **MQTT**: every tag published to the on-board broker as it changes,
  retained, with writes accepted back on a `/set` topic.
- **HTTP** on port 8080: push a new device with `curl`, or read every live
  value as JSON.

Every protocol serves the same values. Write a setpoint over MQTT and read it
back over Modbus; the number is the same, because there is one device
underneath, not four simulators in a trench coat.

## Five minutes, start to finish

```
# write the image to an SD card, boot the Pi, then:
curl http://raspberrypi.local:8080/tags
mosquitto_sub -h raspberrypi.local -t 'silt/#' -v
mbpoll -m tcp -a 1 -0 -t 3 -r 0 -c 4 -1 raspberrypi.local
```

No operating system to install, no packages to add, no Python environment, no
Docker. The image is about 100 MB and boots in a few seconds.

## Your device, not mine

The default is a pump: temperature, pressure, rpm, run hours, ambient
temperature, a fault bit, and a writable setpoint and run flag. It is a
starting point, not the product.

One JSON file describes the whole device. Each tag says how its value moves
and where it appears on every protocol:

```json
{
  "device": "meter-3",
  "tags": [
    { "name": "voltage", "unit": "V", "type": "float",
      "sim": { "sine": { "min": 228, "max": 242, "period_s": 30 } },
      "modbus": { "input": 0, "scale": 10 },
      "opcua": "Voltage",
      "can": { "id": "0x300", "byte": 0, "len": 2, "scale": 10 } }
  ]
}
```

Push it and the device changes in about a second, with no reboot:

```
curl -X POST --data-binary @meter.json http://raspberrypi.local:8080/config
```

If the file has a mistake, you get the reason and the device keeps running
exactly as it was:

```
tags "voltage" and "current" share Modbus address 0
```

That check runs before anything changes, so a bad config is a message rather
than a silent half-applied device.

## Who this is for

- **HMI and SCADA developers** building screens before the panel exists.
- **Gateway and edge developers** who need a southbound device that behaves
  consistently, including on the day the plant network is unavailable.
- **Integrators** testing tag maps, scaling and byte order without booking
  time on a machine.
- **Trainers and students** who need four protocols on one desk for the price
  of a Raspberry Pi.

## What it is not

Being straight about this saves everyone time.

- The servers are **anonymous and unencrypted**. There are no certificates,
  no passwords, and no TLS on any port. This belongs on a lab network, not a
  plant network.
- It is a **simulator**, not a PLC. There is no control logic, no safety
  function, and nothing about it is certified for anything.
- OPC UA support is browse, read, write and subscriptions. There is no
  historical access, no alarms and conditions, and no PubSub yet.
- CAN is a virtual bus out of the box. A real bus needs an MCP2515 HAT and
  one line in a config file.

## Requirements

A Raspberry Pi 3 Model B or B+, a 4 GB or larger SD card, and an Ethernet
cable. Pi 4 and Pi 5 editions are in progress; if that matters to you, say so
and it will move up the list.

## Price

${{price}}, one payment, no subscription. You get the image, the source of the
device daemons, the configuration reference, and the build recipe that
produced the image.

[Buy for ${{price}}]({{buy_url}}){.buy}

## Questions

**Can I use my own register map?**
That is the point. Push your own JSON and the addresses, node names, CAN
frames and MQTT topics are yours.

**Does it work with UaExpert, Ignition, Node-RED, KEPServerEX?**
Any client that speaks plain Modbus TCP, OPC UA without security, or MQTT 3.1.1
can connect. Clients that require an encrypted OPC UA endpoint cannot, yet.

**Can I run several at once?**
Yes. Each Pi is one device. Give them different device names and their MQTT
topics will not collide.

**How do I know what is at which address?**
`GET /tags` prints every tag with its Modbus table and address, OPC UA node
id, CAN frame layout and MQTT topic.

**Can I change it after it boots?**
Over HTTP with `curl`, or by editing the file over SSH. Both take effect in
about a second, without dropping client connections.
