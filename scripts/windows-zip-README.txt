sep2server for Windows (amd64)
==============================

This zip holds sep2server.exe, an IEEE 2030.5 server. The file is unsigned,
so Windows SmartScreen warns on first run: choose "More info", then "Run
anyway". Check the zip against SHA256SUMS from the same release page first.

Environment
-----------
Neither variable is needed just to start the server.

  SEP2_CERT_DIR    Directory holding ca.crt, server.crt and server.key.
                   Default: %USERPROFILE%\tls
  SEP2_ADMIN_KEY   Admin key, at least 16 characters. Required when the
                   admin listener is enabled (SEP2_ADMIN_LISTEN), because
                   Windows builds do not support the admin key file.

Make certificates
-----------------
Run these once, in this order, from a command prompt in this folder. Each
writes into SEP2_CERT_DIR (or %USERPROFILE%\tls):

  sep2server.exe certs generate-ca
  sep2server.exe certs generate-server
  sep2server.exe certs generate-admin
  sep2server.exe certs generate-device --hw-serial INV-001 --name device

generate-ca writes the CA private key, so keep that directory private. To
limit it to your own account (replace the path if you set SEP2_CERT_DIR):

  icacls "%USERPROFILE%\tls" /inheritance:r /grant:r "%USERNAME%:(OI)(CI)F"

Run
---
  sep2server.exe serve

Run "sep2server.exe" with no arguments for the command list.
