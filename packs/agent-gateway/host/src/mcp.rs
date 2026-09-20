//! Just enough MCP for an agent to find the tools and call them.
//!
//! JSON-RPC 2.0 over HTTP POST, which is what an MCP client speaks to a
//! remote server. Hand-written for the same reason as the Modbus client:
//! this is the surface an agent reaches, it is a few hundred lines, and a
//! dependency tree behind it would be a larger thing to trust than the
//! thing it serves.
//!
//! What it does not do is as deliberate as what it does: no sessions, no
//! subscriptions, no server-sent events, no sampling. An agent can list the
//! tools and call one. Everything else is a 404.

use std::io::{BufRead, BufReader, Read, Write};
use std::net::{TcpListener, TcpStream};

pub const PROTOCOL_VERSION: &str = "2025-06-18";

/// What the server can do, supplied by main so this file knows nothing
/// about components or Modbus.
pub trait Tools {
    /// The tool list, already in MCP's shape.
    fn list(&self) -> Result<serde_json::Value, String>;
    /// Call one, returning its JSON result.
    fn call(&self, name: &str, arguments: &str) -> Result<String, String>;
}

pub fn serve(listener: TcpListener, tools: &dyn Tools) {
    for stream in listener.incoming() {
        match stream {
            Ok(s) => {
                if let Err(e) = handle(s, tools) {
                    eprintln!("mcp: {e}");
                }
            }
            Err(e) => eprintln!("mcp: accept: {e}"),
        }
    }
}

fn handle(mut stream: TcpStream, tools: &dyn Tools) -> Result<(), String> {
    let mut reader = BufReader::new(stream.try_clone().map_err(|e| e.to_string())?);
    let mut request_line = String::new();
    if reader.read_line(&mut request_line).map_err(|e| e.to_string())? == 0 {
        return Ok(());
    }
    let mut length = 0usize;
    loop {
        let mut line = String::new();
        if reader.read_line(&mut line).map_err(|e| e.to_string())? == 0 {
            break;
        }
        let line = line.trim_end();
        if line.is_empty() {
            break;
        }
        if let Some(v) = line.strip_prefix("Content-Length:").or_else(|| line.strip_prefix("content-length:")) {
            length = v.trim().parse().unwrap_or(0);
        }
    }
    let mut body = vec![0u8; length];
    reader.read_exact(&mut body).map_err(|e| e.to_string())?;

    let mut parts = request_line.split_whitespace();
    let method = parts.next().unwrap_or("");
    let path = parts.next().unwrap_or("");
    if method != "POST" || !(path == "/" || path == "/mcp") {
        return respond(&mut stream, 404, "{\"error\":\"POST /mcp only\"}");
    }

    let reply = dispatch(&body, tools);
    match reply {
        Some(v) => respond(&mut stream, 200, &v.to_string()),
        // A notification gets no body, as JSON-RPC requires.
        None => respond(&mut stream, 202, ""),
    }
}

fn dispatch(body: &[u8], tools: &dyn Tools) -> Option<serde_json::Value> {
    let req: serde_json::Value = match serde_json::from_slice(body) {
        Ok(v) => v,
        Err(e) => return Some(error(serde_json::Value::Null, -32700, &format!("parse error: {e}"))),
    };
    let id = req.get("id").cloned();
    let method = req.get("method").and_then(|m| m.as_str()).unwrap_or("");
    // No id means a notification: act on nothing, answer nothing.
    let id = match id {
        Some(id) => id,
        None => return None,
    };

    match method {
        "initialize" => Some(result(
            id,
            serde_json::json!({
                "protocolVersion": PROTOCOL_VERSION,
                "capabilities": { "tools": {} },
                "serverInfo": { "name": "silt-agent-gateway", "version": env!("CARGO_PKG_VERSION") },
            }),
        )),
        "tools/list" => match tools.list() {
            Ok(list) => Some(result(id, serde_json::json!({ "tools": list }))),
            Err(e) => Some(error(id, -32603, &e)),
        },
        "tools/call" => {
            let params = req.get("params").cloned().unwrap_or(serde_json::Value::Null);
            let name = params.get("name").and_then(|n| n.as_str()).unwrap_or("");
            let arguments = params
                .get("arguments")
                .cloned()
                .unwrap_or_else(|| serde_json::json!({}))
                .to_string();
            match tools.call(name, &arguments) {
                Ok(text) => Some(result(
                    id,
                    serde_json::json!({ "content": [{ "type": "text", "text": text }], "isError": false }),
                )),
                // A refusal is a result, not a protocol error: the agent
                // asked a well-formed question and the answer is no, with
                // the reason, which is what lets it try something else.
                Err(e) => Some(result(
                    id,
                    serde_json::json!({ "content": [{ "type": "text", "text": e }], "isError": true }),
                )),
            }
        }
        "ping" => Some(result(id, serde_json::json!({}))),
        other => Some(error(id, -32601, &format!("no such method: {other}"))),
    }
}

fn result(id: serde_json::Value, value: serde_json::Value) -> serde_json::Value {
    serde_json::json!({ "jsonrpc": "2.0", "id": id, "result": value })
}

fn error(id: serde_json::Value, code: i32, message: &str) -> serde_json::Value {
    serde_json::json!({ "jsonrpc": "2.0", "id": id, "error": { "code": code, "message": message } })
}

fn respond(stream: &mut TcpStream, status: u16, body: &str) -> Result<(), String> {
    let reason = match status {
        200 => "OK",
        202 => "Accepted",
        _ => "Not Found",
    };
    let head = format!(
        "HTTP/1.1 {status} {reason}\r\nContent-Type: application/json\r\nContent-Length: {}\r\nConnection: close\r\n\r\n",
        body.len()
    );
    stream.write_all(head.as_bytes()).map_err(|e| e.to_string())?;
    stream.write_all(body.as_bytes()).map_err(|e| e.to_string())?;
    stream.flush().map_err(|e| e.to_string())
}
