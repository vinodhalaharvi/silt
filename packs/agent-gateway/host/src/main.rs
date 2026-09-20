//! The host: it serves MCP to an agent and runs the gateway component.
//!
//! What an agent can reach is the gateway's exported tools and nothing
//! else. What the gateway can reach is what this program gives it: one
//! function, silt:modbus/read, backed by a read-only Modbus client. The
//! component gets no filesystem, no sockets, no environment and no clock
//! beyond what is built here, because wasmtime denies by default and this
//! file is the whole of the exception list.
//!
//! So there are two boundaries, and they are checked in different ways.
//! What the component may ask for is in its binary, and silt check verifies
//! it before the image is built. What the host actually provides is here,
//! in a program small enough to read in one sitting.

mod audit;
mod mcp;
mod modbus;

use std::net::TcpListener;
use std::path::PathBuf;
use std::sync::Mutex;
use std::time::Duration;

use wasmtime::component::{Component, HasSelf, Linker, ResourceTable};
use wasmtime::{Config, Engine, Store};
use wasmtime_wasi::{WasiCtx, WasiCtxBuilder, WasiCtxView, WasiView};

wasmtime::component::bindgen!({
    path: "../br2-external/wit",
    world: "gateway",
});

/// What the component gets. WASI is here because Rust's standard library
/// imports it whatever the program does; the parts that matter are the ones
/// not built: no preopened directory, no socket, no environment.
struct Host {
    wasi: WasiCtx,
    table: ResourceTable,
    device: modbus::Device,
}

impl WasiView for Host {
    fn ctx(&mut self) -> WasiCtxView<'_> {
        WasiCtxView { ctx: &mut self.wasi, table: &mut self.table }
    }
}

// The one function the component may call into. A write has no
// implementation here because it has no declaration in the world.
impl silt::modbus::read::Host for Host {
    fn read_holding(&mut self, unit: u8, addr: u16, count: u16) -> Result<Vec<u16>, String> {
        self.device.read_holding(unit, addr, count)
    }
}

/// The component, its store, and the audit log, behind one lock.
///
/// Named Server because bindgen! calls the generated world bindings
/// Gateway, and there should be exactly one Gateway in this program.
///
/// Calls are serialised deliberately: a Modbus device answers one request
/// at a time, and an audit chain has one head. Concurrency here would buy
/// nothing and would make both harder to reason about.
struct Server {
    inner: Mutex<Instance>,
    audit: audit::Audit,
}

struct Instance {
    store: Store<Host>,
    bindings: Gateway,
}

impl mcp::Tools for Server {
    fn list(&self) -> Result<serde_json::Value, String> {
        let mut g = self.inner.lock().unwrap();
        let Instance { store, bindings } = &mut *g;
        let tools = bindings
            .silt_gateway_tools()
            .call_list_tools(&mut *store)
            .map_err(|e| format!("component trapped: {e}"))?;
        Ok(serde_json::Value::Array(
            tools
                .into_iter()
                .map(|t| {
                    serde_json::json!({
                        "name": t.name,
                        "description": t.description,
                        "inputSchema": serde_json::from_str::<serde_json::Value>(&t.input_schema)
                            .unwrap_or(serde_json::Value::Null),
                    })
                })
                .collect(),
        ))
    }

    fn call(&self, name: &str, arguments: &str) -> Result<String, String> {
        let outcome = {
            let mut g = self.inner.lock().unwrap();
            let Instance { store, bindings } = &mut *g;
            bindings
                .silt_gateway_tools()
                .call_call_tool(&mut *store, name, arguments)
                .map_err(|e| format!("component trapped: {e}"))?
        };
        // Recorded before the answer is returned, refusals included: a call
        // that happened without a record is what the log exists to prevent.
        let (state, detail) = match &outcome {
            Ok(text) => ("ok", text.clone()),
            Err(e) => ("refused", e.clone()),
        };
        self.audit.record(name, arguments, state, &detail)?;
        outcome
    }
}

fn env_or(key: &str, default: &str) -> String {
    std::env::var(key).unwrap_or_else(|_| default.to_string())
}

fn main() -> Result<(), Box<dyn std::error::Error>> {
    let component_path = PathBuf::from(env_or("COMPONENT", "/opt/components/gateway.wasm"));
    let listen = env_or("LISTEN", "127.0.0.1:8080");
    let device_addr = env_or("MODBUS_DEVICE", "127.0.0.1:502");
    let audit_path = PathBuf::from(env_or("AUDIT_LOG", "/var/lib/agent-gateway/audit.log"));
    let timeout = Duration::from_millis(
        env_or("MODBUS_TIMEOUT_MS", "2000").parse().unwrap_or(2000),
    );

    let mut config = Config::new();
    config.wasm_component_model(true);
    let engine = Engine::new(&config)?;
    let component = Component::from_file(&engine, &component_path)?;

    let mut linker: Linker<Host> = Linker::new(&engine);
    wasmtime_wasi::p2::add_to_linker_sync(&mut linker)?;
    silt::modbus::read::add_to_linker::<_, HasSelf<Host>>(&mut linker, |h| h)?;

    let host = Host {
        // No .inherit_stdio(), no .preopened_dir(), no .inherit_network():
        // the builder starts closed and nothing here opens it.
        wasi: WasiCtxBuilder::new().build(),
        table: ResourceTable::new(),
        device: modbus::Device::new(device_addr.clone(), timeout),
    };
    let mut store = Store::new(&engine, host);
    let bindings = Gateway::instantiate(&mut store, &component, &linker)?;

    let audit = audit::Audit::open(&audit_path)?;
    println!("component {} ", component_path.display());
    println!("device    {device_addr}");
    println!("audit     {} head {}", audit_path.display(), audit.head());

    let gateway = Server { inner: Mutex::new(Instance { store, bindings }), audit };
    let listener = TcpListener::bind(&listen)?;
    println!("mcp       http://{listen} (protocol {})", mcp::PROTOCOL_VERSION);
    mcp::serve(listener, &gateway);
    Ok(())
}
