<script lang="ts">
  // Shown when the admin session is not (or no longer) valid. This is a
  // real form post to POST /auth/login, not a fetch: that endpoint
  // answers with a 303 to "/" on success and a re-rendered login page on
  // a wrong key, and fetch follows both transparently, so a full-page
  // submit is the only way the browser reliably lands where the server
  // sent it.
  let { reason }: { reason: string } = $props()
</script>

<section class="card" data-testid="login-panel">
  <h2>Admin Login</h2>
  <p class="hint" data-testid="login-reason">{reason}</p>
  <form method="POST" action="/auth/login">
    <div class="form-row">
      <input type="password" id="loginKey" name="key" placeholder="Admin key" autocomplete="current-password" />
      <button class="btn" type="submit">Log in</button>
    </div>
  </form>
</section>
