# GitHub and Google sign-in

Password registration and sign-in remain available. OAuth sign-in is optional
and is enabled per provider by deployment configuration; it does not require
provider secrets in the repository.

Set `GITHUB_OAUTH_CLIENT_ID`, `GITHUB_OAUTH_CLIENT_SECRET`,
`GOOGLE_OAUTH_CLIENT_ID`, and `GOOGLE_OAUTH_CLIENT_SECRET` in the deployment
environment. Set `OAUTH_CALLBACK_BASE_URL` to the public gateway origin (for
example `https://tracker.example`) and register these exact callback URLs with
the providers:

- `${OAUTH_CALLBACK_BASE_URL}/api/auth/oauth/github/callback`
- `${OAUTH_CALLBACK_BASE_URL}/api/auth/oauth/google/callback`

Set `OAUTH_FRONTEND_URL` to the trusted frontend origin. The callback only
redirects to this configured URL and returns an access token in its URL
fragment, which is not sent in HTTP requests or referrer headers. Use HTTPS in
production. OAuth state is signed with `JWT_SECRET`, compared with the provider
callback query, and bound to a short-lived HttpOnly SameSite=Lax cookie.

GitHub uses the provider's stable numeric subject and a verified primary email;
Google requires the verified-email claim from its userinfo endpoint. A new
verified email can create a member account. If the email already belongs to a
password account, OAuth will not merge or sign into it: sign in with the
existing account and use **Link sign-in** in the workspace sidebar. Linked
provider identities are stored only in the Auth service's `auth_db` migration
`000005_oauth_identities`; no other service reads that table. Each account can
link at most one identity per provider, and a provider identity cannot be
linked to multiple accounts.

Provider errors and matching-account conflicts return to the configured
frontend without granting a session. OAuth-only accounts receive a random
unusable local password hash; password login behavior for password-based
accounts remains unchanged.
