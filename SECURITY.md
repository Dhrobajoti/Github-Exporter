# Security Policy

## Supported versions

Security fixes are provided for the latest released version.

## Reporting a vulnerability

Please **do not** open a public issue for security problems.

Report vulnerabilities privately using GitHub's private vulnerability reporting
(**Security** tab of the repository → **Report a vulnerability**), or by email to
`security@example.com`.

Include the affected version, a description of the issue, and steps to reproduce.
You can expect an acknowledgement within a few working days and updates as the
issue is investigated and fixed.

## Handling of credentials

The exporter needs read-only access to security alerts. Follow these practices when
deploying it:

- Prefer a GitHub App over a personal access token, and grant only the read-only
  permissions listed in the README.
- Keep tokens, private keys, TLS keys and the environment file out of version
  control. The repository's `.gitignore` excludes `.env`, `*.pem` and the local
  `config/` directory.
- Restrict file permissions on credential files to the service account that runs
  the exporter.
- Enable HTTPS and basic authentication for the metrics endpoint
  (`WEB_CONFIG_FILE`) whenever it is reachable beyond a trusted host.
