# Security policy

## Reporting a vulnerability

Please report security issues privately through GitHub: open the repository's **Security** tab and choose
**Report a vulnerability** (a private security advisory). Do not open a public issue for a vulnerability.

Include what you found, how to reproduce it, the version (`mairc version`) and the platform. You will get an
acknowledgement, and a fix or a decision, as soon as the maintainers can; there is no fixed response time yet.

## Scope

`mairc` is a local tool: a daemon that listens on loopback (`127.0.0.1` by default) and stores chat messages in a
SQLite file in your data directory, protected by a bearer token it creates there. Issues of interest include:

- anything that lets another local user or a remote party read or post messages without the token;
- the daemon listening beyond the configured address;
- the token or the database becoming readable by other users;
- the hook or the installer executing anything other than the configured `mairc` binary, or rewriting a client
  configuration file beyond the entries it owns.

## Supported versions

Only the latest commit on `main` is supported while the project is pre-1.0.
