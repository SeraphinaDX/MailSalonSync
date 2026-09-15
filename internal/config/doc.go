// Package config loads and validates MailSalonSync's TOML configuration.
//
// The loader is deliberately strict: unknown TOML keys are rejected so a
// misspelled option cannot be silently ignored. Paths are expanded after
// decoding, and protocol-specific defaults are applied during validation.
//
// Secrets may come from a literal value, an environment variable, or a shell
// command. Environment variables and commands are preferred for normal use so
// credentials do not have to be stored in config.toml.
package config
