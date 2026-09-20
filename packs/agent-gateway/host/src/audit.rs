//! An append-only record of what the agent asked for and what it got.
//!
//! Each line carries the SHA-256 of the previous line, so the file is a
//! chain: an edited or deleted record breaks every link after it, and the
//! head hash printed at start-up is what a collector compares against.
//!
//! This is tamper-evident, not tamper-proof, and deliberately so for now.
//! Signing needs a key, a key needs somewhere to live and something to
//! rotate it, and that is a decision about the deployment rather than about
//! this program. The chain is the part that is useful without either.

use std::fs::{File, OpenOptions};
use std::io::{BufRead, BufReader, Write};
use std::path::{Path, PathBuf};
use std::sync::Mutex;

use sha2::{Digest, Sha256};

pub struct Audit {
    path: PathBuf,
    state: Mutex<String>, // hash of the last line written
}

impl Audit {
    /// Open the log, reading the chain's head so appending continues it
    /// rather than starting a second chain in the same file.
    pub fn open(path: &Path) -> Result<Self, String> {
        let mut head = "genesis".to_string();
        if let Ok(f) = File::open(path) {
            for line in BufReader::new(f).lines().map_while(Result::ok) {
                if !line.trim().is_empty() {
                    head = hash(&line);
                }
            }
        }
        if let Some(dir) = path.parent() {
            std::fs::create_dir_all(dir).map_err(|e| format!("{}: {e}", dir.display()))?;
        }
        Ok(Audit { path: path.to_path_buf(), state: Mutex::new(head) })
    }

    pub fn head(&self) -> String {
        self.state.lock().unwrap().clone()
    }

    /// Record one call. Failure to record is failure to serve: a call that
    /// happened without a record is the thing this file exists to prevent.
    pub fn record(&self, tool: &str, arguments: &str, outcome: &str, detail: &str) -> Result<(), String> {
        let mut prev = self.state.lock().unwrap();
        let line = serde_json::json!({
            "prev": *prev,
            "time": now(),
            "tool": tool,
            "arguments": arguments,
            "outcome": outcome,
            "detail": detail,
        })
        .to_string();
        let mut f = OpenOptions::new()
            .create(true)
            .append(true)
            .open(&self.path)
            .map_err(|e| format!("{}: {e}", self.path.display()))?;
        writeln!(f, "{line}").map_err(|e| e.to_string())?;
        f.sync_all().map_err(|e| e.to_string())?;
        *prev = hash(&line);
        Ok(())
    }
}

fn hash(s: &str) -> String {
    let mut h = Sha256::new();
    h.update(s.as_bytes());
    format!("{:x}", h.finalize())
}

fn now() -> u64 {
    std::time::SystemTime::now()
        .duration_since(std::time::UNIX_EPOCH)
        .map(|d| d.as_secs())
        .unwrap_or(0)
}
