// tests/playwright/helpers/hostfs.js
// Maps container media paths (/media/...) to host paths so specs can assert
// the actual bytes a pipeline stage wrote, not just the job record's claims.
//
// The mapping is only knowable when the spec run has the stack's env file:
// tests/e2e.sh exports PELICULA_ENV_FILE (Stage 0) before launching
// Playwright, and that file's LIBRARY_DIR is the host directory mounted at
// /media inside procula and jellyfin. A direct `npx playwright test` against
// some other stack may not have it — hostPathFor returns null then, and
// specs must downgrade their file-content assertions with an explicit skip
// annotation rather than silently passing weaker checks.

const fs = require('fs');

function libraryDirFromEnvFile() {
    const envFile = process.env.PELICULA_ENV_FILE;
    if (!envFile || !fs.existsSync(envFile)) return null;
    for (const line of fs.readFileSync(envFile, 'utf8').split('\n')) {
        const m = line.match(/^LIBRARY_DIR=(.*)$/);
        // Values are written unquoted by the CLI, but tolerate quotes.
        if (m) return m[1].trim().replace(/^["']|["']$/g, '') || null;
    }
    return null;
}

/**
 * Map a container media path to its host path. Returns null when the mapping
 * is unknown (PELICULA_ENV_FILE unset/unreadable, or no LIBRARY_DIR in it)
 * or the path isn't under /media/ — callers distinguish "can't check from
 * this host" (null) from "checked and missing" (fs.existsSync on the result).
 */
function hostPathFor(containerPath) {
    const lib = libraryDirFromEnvFile();
    if (!lib || typeof containerPath !== 'string' || !containerPath.startsWith('/media/')) return null;
    return lib + containerPath.slice('/media'.length);
}

module.exports = { hostPathFor };
