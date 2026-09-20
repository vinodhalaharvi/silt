//! The agent gateway: the tools an AI agent may call on a plant.
//!
//! An agent reaches the plant only through the tools this component exports,
//! and this component reaches the plant only through what it imports. The
//! world in `br2-external/wit/gateway.wit` imports `silt:modbus/read` and
//! nothing that writes, so there is no function in this binary that could
//! change a register — which Silt checks from the binary itself, not from
//! this comment.

wit_bindgen::generate!({
    path: "../../br2-external/wit",
    world: "gateway",
    // Bindings for interfaces from other packages (silt:modbus) are
    // generated here rather than taken from another crate.
    generate_all,
});

use exports::silt::gateway::tools::{Guest, Tool};
use serde::Deserialize;
use silt::modbus::read::read_holding;

const READ_HOLDING: &str = "read_holding_registers";

/// Modbus caps a single read at 125 registers (Modbus Application Protocol
/// v1.1b3, §6.3). Refusing more here gives the agent a clear error instead
/// of an exception code from the device.
const MAX_REGISTERS: u16 = 125;

#[derive(Deserialize)]
#[serde(deny_unknown_fields)]
struct ReadArgs {
    unit: u8,
    address: u16,
    count: u16,
}

struct Gateway;

impl Guest for Gateway {
    fn list_tools() -> Vec<Tool> {
        vec![Tool {
            name: READ_HOLDING.to_string(),
            description: "Read holding registers from a Modbus device. Read-only: \
                          this gateway cannot write. Addresses are protocol \
                          addresses, 0-based and passed to the device unchanged: \
                          documentation that says 40001 means address 0 here, and \
                          40108 means 107. Do not subtract or add an offset."
                .to_string(),
            input_schema: serde_json::json!({
                "type": "object",
                "properties": {
                    "unit": {
                        "type": "integer", "minimum": 0, "maximum": 255,
                        "description": "Modbus unit (slave) id. Behind a TCP-to-RTU \
                                        gateway this selects a device on the serial segment."
                    },
                    "address": {
                        "type": "integer", "minimum": 0, "maximum": 65535,
                        "description": "Protocol address, 0-based, sent to the device as \
                                        given. NOT 4xxxx notation: 40001 in documentation \
                                        is address 0 here."
                    },
                    "count": {
                        "type": "integer", "minimum": 1, "maximum": MAX_REGISTERS,
                        "description": "How many consecutive registers to read. Modbus \
                                        caps a single read at 125."
                    }
                },
                "required": ["unit", "address", "count"],
                "additionalProperties": false
            })
            .to_string(),
        }]
    }

    fn call_tool(name: String, arguments: String) -> Result<String, String> {
        match name.as_str() {
            READ_HOLDING => read_registers(&arguments),
            // Unknown names are refused by name, so an agent probing for a
            // write tool learns that none exists rather than seeing a crash.
            other => Err(format!("no such tool: {other}")),
        }
    }
}

fn read_registers(arguments: &str) -> Result<String, String> {
    let args: ReadArgs =
        serde_json::from_str(arguments).map_err(|e| format!("invalid arguments: {e}"))?;
    if args.count == 0 || args.count > MAX_REGISTERS {
        return Err(format!("count must be 1..={MAX_REGISTERS}, got {}", args.count));
    }
    if u32::from(args.address) + u32::from(args.count) > 0x1_0000 {
        return Err("address + count runs past register 65535".to_string());
    }
    let values = read_holding(args.unit, args.address, args.count)?;
    // The answer says which addressing it used. A caller that assumed 4xxxx
    // notation read a different part of the map and got a plausible-looking
    // answer; saying so in the reply is what lets it notice.
    Ok(serde_json::json!({
        "unit": args.unit,
        "address": args.address,
        "addressing": "protocol, 0-based",
        "values": values,
    })
    .to_string())
}

export!(Gateway);
