//! The tray updater manifest's own signature. Tauri checks each bundle's
//! signature, but takes the version to install from the manifest, which it
//! doesn't verify, so whoever controls the update host could offer an old,
//! validly signed bundle as a newer version. The release signs the manifest
//! too: tray/<channel>.json.minisig, with the trusted comment
//! "switchboard-tray <channel> <version>" (TrayManifestComment in
//! internal/update). This checks it before anything is installed.

use minisign_verify::{PublicKey, Signature};
use serde_json::Value;

/// The trusted comment the manifest's signature must carry.
pub fn expected_comment(channel: &str, version: &str) -> String {
    format!("switchboard-tray {channel} {version}")
}

/// The updater's public key from its config value: base64 of the whole
/// minisign .pub file, as Tauri takes it.
pub fn public_key(config_pubkey: &str) -> Result<PublicKey, String> {
    let pub_file = base64_decode(config_pubkey)
        .and_then(|b| String::from_utf8(b).ok())
        .ok_or("the updater public key isn't base64")?;
    PublicKey::decode(&pub_file).map_err(|e| format!("the updater public key isn't a minisign key: {e}"))
}

/// The channel an update endpoint serves: "stable" for .../tray/stable.json.
pub fn channel(endpoint: &str) -> Option<&str> {
    let name = endpoint.rsplit('/').next()?.strip_suffix(".json")?;
    (!name.is_empty() && name.bytes().all(|b| b.is_ascii_alphanumeric() || b == b'-')).then_some(name)
}

/// Verifies `manifest`, the exact bytes fetched from the channel's endpoint,
/// against `sig`, its .minisig file: the signature must be made with `pk`,
/// and its trusted comment must name `channel` and the manifest's own
/// version. The stable channel may not offer a pre-release. Returns the
/// manifest.
pub fn verify(pk: &PublicKey, channel: &str, manifest: &[u8], sig: &str) -> Result<Value, String> {
    let sig = Signature::decode(sig).map_err(|e| format!("the update manifest's signature is malformed: {e}"))?;
    pk.verify(manifest, &sig, false)
        .map_err(|e| format!("the update manifest's signature doesn't verify: {e}"))?;
    let json: Value = serde_json::from_slice(manifest).map_err(|e| format!("the update manifest isn't JSON: {e}"))?;
    let version = json["version"].as_str().ok_or("the update manifest has no version")?;
    let want = expected_comment(channel, version);
    if sig.trusted_comment() != want {
        return Err(format!(
            "the update manifest is signed as \"{}\", not \"{want}\"",
            sig.trusted_comment()
        ));
    }
    if channel == "stable" && version.contains('-') {
        return Err(format!("the stable channel offers pre-release {version}"));
    }
    Ok(json)
}

/// Checks that the update Tauri is about to install comes from the manifest
/// that was verified, byte for byte as JSON, and is exactly its version, so
/// a host that answers the second fetch differently gets nowhere.
pub fn matches(signed: &Value, tauri_manifest: &Value, tauri_version: &str) -> Result<(), String> {
    if signed != tauri_manifest {
        return Err("the update manifest changed between checking its signature and reading it".into());
    }
    let signed_version = signed["version"].as_str().unwrap_or_default();
    if tauri_version.trim_start_matches('v') != signed_version.trim_start_matches('v') {
        return Err(format!(
            "the updater offers version {tauri_version}, but the manifest is signed for {signed_version}"
        ));
    }
    Ok(())
}

/// Standard base64, padding optional and whitespace ignored. None for any
/// other character.
fn base64_decode(s: &str) -> Option<Vec<u8>> {
    let mut out = Vec::with_capacity(s.len() * 3 / 4);
    let (mut acc, mut bits) = (0u32, 0u32);
    for c in s
        .trim_end()
        .trim_end_matches('=')
        .bytes()
        .filter(|b| !b.is_ascii_whitespace())
    {
        let v = match c {
            b'A'..=b'Z' => c - b'A',
            b'a'..=b'z' => c - b'a' + 26,
            b'0'..=b'9' => c - b'0' + 52,
            b'+' => 62,
            b'/' => 63,
            _ => return None,
        };
        acc = (acc << 6) | u32::from(v);
        bits += 6;
        if bits >= 8 {
            bits -= 8;
            out.push((acc >> bits) as u8);
            acc &= (1 << bits) - 1;
        }
    }
    Some(out)
}

#[cfg(test)]
mod tests {
    use super::*;

    // A real signature made with `tauri signer sign` and a throwaway key whose
    // secret half was deleted; the same fixture internal/update tests.
    const PUB_FILE: &str = include_str!("../../../../internal/update/testdata/tauri-signer.pub");
    const MESSAGE: &[u8] = include_bytes!("../../../../internal/update/testdata/tauri-signer-message.txt");
    const SIG: &str = include_str!("../../../../internal/update/testdata/tauri-signer-message.txt.minisig");
    /// `base64 < tauri-signer.pub`, as TAURI_UPDATER_PUBKEY holds it.
    const CONFIG_PUBKEY: &str = "dW50cnVzdGVkIGNvbW1lbnQ6IG1pbmlzaWduIHB1YmxpYyBrZXk6IEY5OUUyNUYyQUI5QUJCMDIKUldRQ3U1cXI4aVdlK2JIQzdNVnB4bW9BbjcxOU14VGE0SnZ1dEJka2ZqL0FiQ1FKVk9DbWZJc1oK";

    /// Must match TrayManifestComment in internal/update (TestCheckSignedTrayManifest).
    #[test]
    fn comment_matches_sb_manifest() {
        assert_eq!(expected_comment("stable", "1.4.0"), "switchboard-tray stable 1.4.0");
    }

    #[test]
    fn decodes_the_config_key() {
        assert_eq!(public_key(CONFIG_PUBKEY).unwrap(), PublicKey::decode(PUB_FILE).unwrap());
        // Line-wrapped, as `base64` prints it on Linux.
        let wrapped = format!("{}\n{}\n", &CONFIG_PUBKEY[..76], &CONFIG_PUBKEY[76..]);
        assert_eq!(public_key(&wrapped).unwrap(), PublicKey::decode(PUB_FILE).unwrap());
        for bad in ["", "not base64!", "aGVsbG8="] {
            assert!(public_key(bad).is_err(), "{bad:?} accepted");
        }
    }

    #[test]
    fn base64() {
        for (enc, dec) in [
            ("", ""),
            ("Zg==", "f"),
            ("Zm8=", "fo"),
            ("Zm9v", "foo"),
            ("Zm9vYmFy", "foobar"),
            ("Zm9vYg", "foob"),
        ] {
            assert_eq!(base64_decode(enc).unwrap(), dec.as_bytes(), "{enc}");
        }
        assert!(base64_decode("Zm9v-").is_none());
    }

    #[test]
    fn channels() {
        for (url, want) in [
            (
                "https://nanaaikinson.github.io/switchboard-updates/tray/stable.json",
                Some("stable"),
            ),
            ("https://example.com/tray/beta.json", Some("beta")),
            ("https://example.com/tray/stable.json?x=1", None),
            ("https://example.com/tray/", None),
            ("https://example.com/tray/.json", None),
        ] {
            assert_eq!(channel(url), want, "{url}");
        }
    }

    #[test]
    fn verifies_signatures() {
        let pk = public_key(CONFIG_PUBKEY).unwrap();
        // The fixture isn't a manifest, and its comment is Tauri's, so a
        // valid signature still stops at the JSON or the comment.
        let err = verify(&pk, "stable", MESSAGE, SIG).unwrap_err();
        assert!(err.contains("isn't JSON"), "{err}");

        let mut tampered = MESSAGE.to_vec();
        tampered.push(b'!');
        let err = verify(&pk, "stable", &tampered, SIG).unwrap_err();
        assert!(err.contains("doesn't verify"), "{err}");

        let changed_comment = SIG.replace("file:message.txt", "switchboard-tray stable 99.0.0");
        let err = verify(&pk, "stable", MESSAGE, &changed_comment).unwrap_err();
        assert!(err.contains("doesn't verify"), "{err}");

        let err = verify(&pk, "stable", MESSAGE, "not a signature").unwrap_err();
        assert!(err.contains("malformed"), "{err}");

        // Another key with the same ID: the signature doesn't verify.
        let other = PublicKey::from_base64("RWQCu5qr8iWe+bAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA").unwrap();
        assert!(verify(&other, "stable", MESSAGE, SIG).is_err());
    }

    #[test]
    fn checks_what_tauri_installs() {
        let signed: Value = serde_json::json!({"version": "1.4.0", "rollout_percent": 100, "platforms": {}});
        assert!(matches(&signed, &signed.clone(), "1.4.0").is_ok());
        assert!(matches(&signed, &signed.clone(), "v1.4.0").is_ok());
        let err = matches(&signed, &signed.clone(), "99.0.0").unwrap_err();
        assert!(err.contains("signed for 1.4.0"), "{err}");
        let mut other = signed.clone();
        other["rollout_percent"] = 50.into();
        let err = matches(&signed, &other, "1.4.0").unwrap_err();
        assert!(err.contains("changed"), "{err}");
    }
}
