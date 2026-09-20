//! A first component: it counts its boots in the one directory it is granted.
//!
//! The unit runs it as `wasmtime run --dir /var/lib/component::/data
//! -S inherit-network=n`, so /data is the only path it can see, and it is
//! backed by /var/lib/component on the appliance. A count that climbs across
//! reboots shows the grant works; nothing else on the filesystem is visible
//! to it at all.
//!
//! Try changing it. Open a socket and silt check will refuse the image,
//! because this pack's policy forbids wasi:sockets — checked from the binary,
//! before anything builds. Read a path outside /data and it fails at run
//! time, because that path does not exist in the component's world.

use std::fs;

const COUNTER: &str = "/data/boots";

fn main() {
    let boots = fs::read_to_string(COUNTER)
        .ok()
        .and_then(|s| s.trim().parse::<u64>().ok())
        .unwrap_or(0)
        + 1;

    match fs::write(COUNTER, boots.to_string()) {
        Ok(()) => println!("hello from a wasm component: boot #{boots}"),
        Err(e) => {
            eprintln!("hello: cannot write {COUNTER}: {e}");
            std::process::exit(1);
        }
    }

    // What the component can see: /data and nothing else.
    match fs::read_dir("/") {
        Ok(entries) => {
            let names: Vec<String> = entries
                .filter_map(|e| e.ok())
                .map(|e| e.file_name().to_string_lossy().into_owned())
                .collect();
            println!("hello: visible at /: {}", names.join(" "));
        }
        Err(e) => println!("hello: / is not listable here: {e}"),
    }
}
