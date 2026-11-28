# Final review of the web foundation (Plan 8a, Tasks 1 to 8): findings to fix

Verdict: changes required. Nothing critical.

## Important

1. **Flaky test helper.** `web/src/test/render.tsx:27-33`: `LocationProbe` reports through `useEffect`, so an assertion on `location()` can run before it. Seen failing in `web/src/auth/SignIn.test.tsx:66` (expected `/`, got `/sign-in`). Use `useLayoutEffect`, or make `location()` assertions wait. Every `location()` assertion after a navigation is exposed.
2. **Loading tests prove nothing.** `JobPage.test.tsx:68`, `InventoryPage.test.tsx:252`, `JobsPage.test.tsx:252`, `UsersSection.test.tsx:65`: `getByRole('status')` matches the session's "Loading your session" line, so removing `<Loading />` from the screens leaves all tests green. Each test must assert the screen's own loading state (query within the screen's region, after the session has loaded) and must fail when the screen's `<Loading />` is removed. Check the other screens' loading tests for the same mistake.
3. **Sideways page scroll at 380px.** CLI page by 187px, Groups by 51px. `.ui-visually-hidden` (`web/src/ui/Loading.css:12`) is `position:absolute` inside `.ui-table-scroll` (`web/src/ui/DataTable.css:1`), which is not positioned, so the hidden header escapes the scroll box. Give the wrapper `position: relative`. Add a test or a documented check that the document does not overflow at 360px for these two pages.
4. **A `null` list blanks the whole app.** No error boundary exists. `web/src/auth/permissions.ts:18` calls `me.grants.some`; `"grants": null` throws in the sidebar and leaves a white page. Same risk for `user.groups`, `account.environments` and every list wrapper. Normalise `null` to `[]` at the API boundary for every list field in `web/src/api/` (one helper, applied in the hooks or the client), and add an error boundary in the shell that shows a readable "Something went wrong" with a reload button. Tests: `me` with `grants: null` renders the app; a component that throws shows the boundary, not a blank page.
5. **SSO sign-in loses the return page.** `web/src/auth/SignIn.tsx:83` links to `/api/v1/auth/saml/start` with no return path, so `/cli/confirm?code=...` and any mid-session 401 lose their place for SSO users. Link to `/api/v1/auth/saml/start?next=<safe path>` (the backend will honour a same-site path), using the same `safeNext` rule.

## Minor

- `Sidebar.tsx:35`: show Admin only when the person can open at least one Admin section (`manage_access`, `manage_connections`, `view_audit`, or admin). `manage_jobs` alone does not qualify.
- AWS accounts table at 1360px: the Test action is off-screen until the card is scrolled; put the action column first or make it sticky at the right.
- `ProblemList.tsx:20`: a whole-file problem (line 0) shows `environments.yaml:0`; show the file name alone.
- `me` is never refetched: refetch it after any change to users, groups, grants or mappings.
- A 409 on a user PATCH always shows the last-admin message; use the server's message, and the last-admin wording only when the server's code or message says so.
- `SSOSection.tsx:126` uses the glyphs ✓ and ✕; use the kit's status shapes and words.
- Enable/disable buttons on all rows spin together (shared `update.isPending`); track the pending row.

## Field names the web app assumes for routes that do not exist yet

Every list must be `[]`, never `null`.

- `GET /api/v1/me` and the login response: `id, email, name, admin, grants[{action, label, environment, folder_id}]`; `""` or `0` means any.
- `GET /api/v1/auth/methods`: `{password, saml}`.
- `POST /api/v1/auth/login`: `{email, password}`; 429 with a numeric `Retry-After` in seconds. `POST /api/v1/auth/logout`.
- `/api/v1/admin/users`: `{users:[{id, email, name, source: local|saml|service, disabled, groups:[{id, name}], last_sign_in_at, created_at}]}`; POST `{email, name, source, password}`; PATCH `{disabled?}` or `{group_ids?}`; 409 for the last admin.
- `/api/v1/admin/groups`: `{groups:[{id, name, admin, member_count}]}`; POST `{name}`; PATCH `{admin}`.
- `/api/v1/admin/grants`: `{grants:[{id, group_id, action, label, environment, folder_id}]}`; POST `{group_id, action, label?, environment?, folder_id?}`; DELETE `/{id}`.
- `/api/v1/admin/idp-mappings`: `{mappings:[{idp_group, group_id}]}`; PUT and DELETE `/{idp_group}` (URL-encoded), PUT body `{group_id}`.
- `/api/v1/admin/audit?limit=25&before=<id>`: `{entries:[{id, at, actor, action, target_type, target_id, detail}], next_before}`.
- `/api/v1/admin/sso` GET and PUT: `{enabled, idp_metadata_xml, entity_id, groups_attribute, email_attribute, name_attribute, acs_url, metadata_url}` (the last two read-only); `POST /api/v1/admin/sso/test` returns `{ok, message}`.
- `/api/v1/tokens`: `{tokens:[{id, name, created_at, expires_at, last_used_at}]}`; POST `{name, ttl_days}` returns the same plus `plaintext`; DELETE `/{id}`.
- `POST /api/v1/cli/login/confirm`: `{user_code}`.
- `/api/v1/admin/aws-accounts`: `{accounts:[{id, name, account_id, discovery, deploy, environments[], created_at}]}`, credential view `{method: role|key, role_arn, external_id, access_key_id_last4}`; POST `{name, account_id, discovery, deploy}` with input `{method, role_arn?, external_id?, access_key_id?, secret_access_key?}`; `POST /{id}/test` returns `{ok, error_code, message}`.

## Backend additions the web app is waiting for (Plan 8a Task 9 and Plan 3)

- `GET /api/v1/auth/methods`.
- `source` (raw YAML) on the job and service responses.
- `services` (count or names) and `approval_labels` on each item of `GET /api/v1/jobs`.
- A GET for the inventory source (repo, branch, path).
- `GET /api/v1/auth/saml/start?next=<path>` honouring a same-site path after sign-in.
