---
title: How to test an HMI without a PLC
description: Five ways to get Modbus, OPC UA or CAN data to develop against
  when the panel does not exist yet, and what each one costs you in setup time
  and realism.
kind: article
date: 2026-09-22
---

# How to test an HMI without a PLC

The screens are due before the panel is built, or the PLC exists but lives in
a machine nobody will stop for you. Either way you need something that answers
Modbus, OPC UA or CAN so the work can continue.

There are five common answers. None is wrong; they cost different things.

## 1. A software simulator on your laptop

`pymodbus` ships a server. open62541 and the Eclipse Milo project both have
server examples. Free, and running in an afternoon.

What it costs: it is a program on your development machine, so it is not on
the network the way a device is, it stops when you close the laptop, and its
address is your laptop's. For a colleague to point their client at it you have
to keep it running and share an address that changes. It also tests your client
against localhost, which hides timeouts, reconnects and everything else that
appears once a network is involved.

## 2. A commercial simulator

Witte Software's Modbus Slave, ModbusManager, Prosys' OPC UA simulator. Mature,
configurable, and support you can call. Modbus Slave is $129 for a single
licence; ModbusManager's Standard edition is $49.

What it costs: money, usually one protocol per product, and usually Windows.
Multi-protocol testing means several tools that do not share a value, so a
setpoint you change in one is not visible in the others.

## 3. A spare PLC

The most realistic option, because it is the real thing.

What it costs: hardware, the vendor's programming software, a licence, and the
time to write logic whose only purpose is to make numbers move. It also
occupies a bench.

## 4. Node-RED with protocol nodes

Flexible, free, and good if you already run it. Nodes exist for Modbus, OPC UA,
MQTT and CAN.

What it costs: you are now maintaining a Node-RED instance, its Node.js
runtime and a set of contributed nodes, and you build the simulated behaviour
by wiring flows. It drifts as the nodes update. Good for glue, heavier than it
looks as a permanent test target.

## 5. A dedicated appliance

A small board that boots into a simulated device and stays on the network. A
Raspberry Pi is enough.

What it costs: a board, and the evening it takes to assemble an image, unless
you buy one already built. In exchange it behaves like equipment: it has its
own address, it is there when you arrive, colleagues can point clients at it,
and it survives your laptop rebooting.

## What to look for, whichever you pick

- **One set of values across protocols.** If a setpoint written over MQTT does
  not show up on Modbus, you are testing several simulators rather than one
  device, and integration bugs hide in that gap.
- **Values that move.** A constant tells you a read succeeded. A value that
  changes tells you your subscription, polling loop and screen refresh work.
- **Honest errors.** A real device answers `ILLEGAL DATA ADDRESS` for a
  register it does not have. A simulator that returns 0 for every address will
  let you ship a wrong tag map and find out in commissioning.
- **Configuration you can change quickly.** You will iterate on the tag map
  more often than you expect, and an editor plus a reboot per change is a tax
  on the whole project.
- **Scaling and byte order.** Real devices carry 54.3 as 543, and CAN signals
  come in both byte orders. If your test target ignores those, the first real
  device will not.

## What we built

[{{product}}](/) is option five, prepared: a Raspberry Pi image that boots
into a device answering Modbus TCP, OPC UA, CAN and MQTT at once, all from one
JSON file you push over HTTP. It is ${{price}}.

It is not a PLC and does not pretend to be one. It is the thing you point a
client at while the PLC does not exist.
