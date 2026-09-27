---
title: Notes — Fieldbus Sim Appliance
description: Write-ups from building an industrial protocol appliance on
  Buildroot: OPC UA, Modbus, CAN, and the failure modes that cost an evening.
kind: page
date: 2026-09-22
---

# Notes

Things that went wrong while building this, written down in case you are
searching for the same symptom at midnight.

- [An OPC UA server that listens only on IPv6](/blog/opc-ua-listening-ipv6-only/)
  — the process is running, the port is open, and every client gets connection
  refused.
- [Buildroot ignored my new defconfig](/blog/buildroot-defconfig-ignored/) —
  you changed the defconfig, ran make, and the image came out the same.
- [How to test an HMI without a PLC](/blog/test-hmi-without-plc/) — five
  options, and what each one costs you.
