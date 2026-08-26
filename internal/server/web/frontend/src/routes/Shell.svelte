<script lang="ts">
  // Shell is the admin UI's landing panel while the extension contract
  // (Phase 5) and the dashboard rewrite are still ahead: this phase ships
  // the toolchain and the fallback plumbing only, so the panel states that
  // plainly and links back to the existing dashboard rather than
  // duplicating any of its data.
</script>

<section class="panel" data-testid="shell-panel">
  <h1>IEEE 2030.5 Server Admin</h1>
  <p data-testid="shell-status">Admin UI shell is live. Panels are added here as they are built.</p>
  <p>
    <a data-testid="dashboard-link" href="/">Existing dashboard</a>
  </p>
</section>

<style>
  .panel {
    max-width: 720px;
    margin: 0 auto;
    padding: 32px 20px;
  }
</style>
