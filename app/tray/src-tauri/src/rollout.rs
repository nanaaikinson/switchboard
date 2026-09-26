//! Staged rollouts, computed exactly as sb does (internal/update/rollout.go),
//! from the same install ID in the Switchboard config dir.

use std::path::Path;

use sha2::{Digest, Sha256};

/// Reads the install ID from dir/install-id, creating it on first use. It
/// only decides the rollout bucket and is never sent anywhere.
pub fn install_id(dir: &Path) -> std::io::Result<String> {
    let path = dir.join("install-id");
    if let Ok(s) = std::fs::read_to_string(&path) {
        let id = s.trim();
        if id.len() == 32 && id.bytes().all(|b| b.is_ascii_hexdigit()) {
            return Ok(id.to_owned());
        }
    }
    let mut raw = [0u8; 16];
    getrandom::fill(&mut raw).map_err(|e| std::io::Error::other(e.to_string()))?;
    let id: String = raw.iter().map(|b| format!("{b:02x}")).collect();
    std::fs::create_dir_all(dir)?;
    write_private(&path, format!("{id}\n").as_bytes())?;
    Ok(id)
}

#[cfg(unix)]
pub(crate) fn write_private(path: &Path, data: &[u8]) -> std::io::Result<()> {
    use std::io::Write;
    use std::os::unix::fs::OpenOptionsExt;
    let mut f = std::fs::OpenOptions::new()
        .write(true)
        .create(true)
        .truncate(true)
        .mode(0o600)
        .open(path)?;
    f.write_all(data)
}

#[cfg(not(unix))]
pub(crate) fn write_private(path: &Path, data: &[u8]) -> std::io::Result<()> {
    std::fs::write(path, data)
}

/// The install's bucket, 0-99, for a release: stable per install and
/// version, reshuffled between versions.
pub fn bucket(install_id: &str, version: &str) -> u32 {
    let mut h = Sha256::new();
    h.update(install_id.as_bytes());
    h.update([0u8]);
    h.update(version.trim_start_matches('v').as_bytes());
    let d = h.finalize();
    u32::from_be_bytes([d[0], d[1], d[2], d[3]]) % 100
}

pub fn in_rollout(bucket: u32, percent: u32) -> bool {
    bucket < percent
}

#[cfg(test)]
mod tests {
    use super::*;

    /// Must match TestBucket in internal/update, or the tray and sb would
    /// disagree about who's in a rollout.
    #[test]
    fn matches_sb() {
        let id = "00000000000000000000000000000001";
        assert_eq!(bucket(id, "1.0.0"), 9);
        assert_eq!(bucket(id, "v1.0.0"), 9);
        assert_eq!(bucket(id, "2.0.0"), 12);
    }

    #[test]
    fn rollout_edges() {
        assert!(!in_rollout(0, 0));
        assert!(in_rollout(0, 1) && !in_rollout(1, 1));
        assert!(in_rollout(99, 100));
    }

    #[test]
    fn install_id_persists() {
        let dir = std::env::temp_dir().join(format!("sb-tray-id-{}", std::process::id()));
        let _ = std::fs::remove_dir_all(&dir);
        let id = install_id(&dir).unwrap();
        assert_eq!(id.len(), 32);
        assert_eq!(install_id(&dir).unwrap(), id);
        std::fs::write(dir.join("install-id"), "garbage").unwrap();
        assert_ne!(install_id(&dir).unwrap(), id);
        let _ = std::fs::remove_dir_all(&dir);
    }
}
