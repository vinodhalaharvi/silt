//! Modbus TCP, reading only.
//!
//! Hand-written rather than pulled in, because this is the only code in the
//! appliance that touches the plant's network and it is 60 lines: a client
//! that can only issue function code 3 cannot be talked into writing a
//! register by a bug in a dependency's write path, because it has none.

use std::io::{Read, Write};
use std::net::TcpStream;
use std::time::Duration;

/// Function code 3: read holding registers. The only one this client has.
const READ_HOLDING: u8 = 3;

pub struct Device {
    addr: String,
    timeout: Duration,
}

impl Device {
    pub fn new(addr: String, timeout: Duration) -> Self {
        Device { addr, timeout }
    }

    /// Read `count` holding registers starting at `addr` from `unit`.
    pub fn read_holding(&self, unit: u8, addr: u16, count: u16) -> Result<Vec<u16>, String> {
        if count == 0 || count > 125 {
            return Err(format!("count must be 1..=125, got {count}"));
        }
        // MBAP header: transaction, protocol 0, length, unit; then the PDU.
        let mut req = Vec::with_capacity(12);
        req.extend_from_slice(&1u16.to_be_bytes()); // transaction id
        req.extend_from_slice(&0u16.to_be_bytes()); // protocol id
        req.extend_from_slice(&6u16.to_be_bytes()); // bytes after this field
        req.push(unit);
        req.push(READ_HOLDING);
        req.extend_from_slice(&addr.to_be_bytes());
        req.extend_from_slice(&count.to_be_bytes());

        let mut sock = TcpStream::connect(&self.addr).map_err(|e| format!("connect {}: {e}", self.addr))?;
        sock.set_read_timeout(Some(self.timeout)).map_err(|e| e.to_string())?;
        sock.set_write_timeout(Some(self.timeout)).map_err(|e| e.to_string())?;
        sock.write_all(&req).map_err(|e| format!("write: {e}"))?;

        let mut head = [0u8; 8];
        sock.read_exact(&mut head).map_err(|e| format!("read header: {e}"))?;
        let len = u16::from_be_bytes([head[4], head[5]]) as usize;
        if len < 2 {
            return Err(format!("short reply: length {len}"));
        }
        let mut body = vec![0u8; len - 2];
        sock.read_exact(&mut body).map_err(|e| format!("read body: {e}"))?;

        // An exception reply sets the high bit of the function code and
        // carries one byte saying why.
        let func = head[7];
        if func & 0x80 != 0 {
            let code = body.first().copied().unwrap_or(0);
            return Err(format!("device refused: exception {code}"));
        }
        if func != READ_HOLDING {
            return Err(format!("device answered function {func}, not {READ_HOLDING}"));
        }
        let want = usize::from(count) * 2;
        if body.first().copied().unwrap_or(0) as usize != want || body.len() < want + 1 {
            return Err("device returned the wrong number of bytes".to_string());
        }
        Ok(body[1..=want]
            .chunks(2)
            .map(|c| u16::from_be_bytes([c[0], c[1]]))
            .collect())
    }
}
