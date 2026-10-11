//! Zed/Gram extension that runs bls for languages without a language server of their own.

use zed_extension_api::{self as zed, serde_json, settings::LspSettings, LanguageServerId, Result};

struct Bls;

/// Maps a server id from extension.toml to the bls subcommand.
fn mode(id: &str) -> Option<&'static str> {
    match id {
        "bls-ini" => Some("ini"),
        "bls-toml" => Some("toml"),
        "bls-apache" => Some("apache"),
        _ => None,
    }
}

impl zed::Extension for Bls {
    fn new() -> Self {
        Bls
    }

    fn language_server_command(
        &mut self,
        id: &LanguageServerId,
        worktree: &zed::Worktree,
    ) -> Result<zed::Command> {
        let mode = mode(id.as_ref()).ok_or_else(|| format!("unknown language server {id}"))?;
        let binary = LspSettings::for_worktree(id.as_ref(), worktree)
            .ok()
            .and_then(|s| s.binary);
        let path = binary
            .as_ref()
            .and_then(|b| b.path.clone())
            .or_else(|| worktree.which("bls"))
            .ok_or_else(|| {
                format!("bls not found on PATH; install it or set lsp.{id}.binary.path in settings")
            })?;
        let args = binary
            .and_then(|b| b.arguments)
            .unwrap_or_else(|| vec![mode.to_string()]);
        Ok(zed::Command {
            command: path,
            args,
            env: worktree.shell_env(),
        })
    }

    fn language_server_workspace_configuration(
        &mut self,
        id: &LanguageServerId,
        worktree: &zed::Worktree,
    ) -> Result<Option<serde_json::Value>> {
        Ok(LspSettings::for_worktree(id.as_ref(), worktree)
            .ok()
            .and_then(|s| s.settings))
    }
}

zed::register_extension!(Bls);
