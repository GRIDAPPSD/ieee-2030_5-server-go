<script lang="ts">
    import { onMount } from 'svelte'
    import SwaggerUI from 'swagger-ui-dist/swagger-ui-es-bundle.js'
    import 'swagger-ui-dist/swagger-ui.css'

    onMount(() => {
        SwaggerUI({
            dom_id: '#swagger-ui',
            url: '/ui/openapi.yaml',
        })
    })
</script>

<div class="swagger-page">
    <div id="swagger-ui"></div>
</div>

<style>
    .swagger-page {
        min-height: 100vh;
        background: white;
        color: #3b4151;
    }
</style>