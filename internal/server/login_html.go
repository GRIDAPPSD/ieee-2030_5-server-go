package server

// loginHTML is the plain HTML form served at GET /login. The form POSTs to
// /auth/login. On success the server sets the admin_ticket cookie and
// redirects to /. This file deliberately ships no external JS/CSS — the
// page is operator tooling and stays static.
const loginHTML = `<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="UTF-8">
<meta name="viewport" content="width=device-width, initial-scale=1.0">
<title>IEEE 2030.5 Admin Login</title>
<style>
  body { font-family: -apple-system, BlinkMacSystemFont, 'Segoe UI', sans-serif; background: #0f172a; color: #e2e8f0; display: flex; align-items: center; justify-content: center; min-height: 100vh; margin: 0; }
  .card { background: #1e293b; border: 1px solid #334155; border-radius: 8px; padding: 32px; width: 360px; }
  h1 { font-size: 18px; color: #3b82f6; margin-bottom: 4px; }
  p { font-size: 13px; color: #94a3b8; margin-bottom: 16px; }
  label { display: block; font-size: 12px; color: #94a3b8; margin-bottom: 6px; text-transform: uppercase; letter-spacing: 0.05em; }
  input[type="password"] { width: 100%; background: #0f172a; border: 1px solid #334155; color: #e2e8f0; padding: 10px; border-radius: 4px; font-size: 14px; box-sizing: border-box; }
  button { margin-top: 16px; width: 100%; background: #3b82f6; color: white; border: none; padding: 10px; border-radius: 4px; cursor: pointer; font-size: 14px; }
  button:hover { background: #2563eb; }
  .error { margin-top: 12px; color: #ef4444; font-size: 13px; min-height: 18px; }
</style>
</head>
<body>
<form class="card" method="POST" action="/auth/login" autocomplete="off">
  <h1>IEEE 2030.5 Admin</h1>
  <p>Sign in to access the server dashboard.</p>
  <label for="key">Admin Key</label>
  <input id="key" name="key" type="password" required autofocus>
  <button type="submit">Sign in</button>
  <div class="error">{{ERROR}}</div>
</form>
</body>
</html>`
