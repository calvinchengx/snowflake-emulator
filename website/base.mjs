// WHERE THE SITE LIVES, defined once.
//
// This was three constants -- astro.config.mjs, scripts/sync-docs.mjs and
// scripts/parity-versions.mjs each carried their own copy -- and moving the
// docs under /docs/ updated one of them. The build stayed green and the pages
// were all still generated; every link the two scripts wrote just pointed one
// directory above where the pages now were. Nothing goes red for that. The
// reader gets a 404.
//
// Starlight prefixes generated hrefs with the Astro `base`, and the two
// scripts write absolute links by hand, so all three have to be the same
// string. Now they are the same string.
export const BASE = '/snowflake-emulator/docs/';
